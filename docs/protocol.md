# Protocol

QDAY Stratum implements the SiaMining variant of Stratum over newline-delimited
JSON-RPC. It translates a fixed, transaction-aware QDAY block template into the
job format already understood by Sia BLAKE2b miners.

The canonical upstream description is the
[SiaMining Stratum specification](https://github.com/SiaMining/Stratum/blob/master/Stratum.md).

## Connection sequence

The miner sends:

```json
{"id":1,"method":"mining.subscribe","params":["gominer"]}
```

The bridge assigns a four-byte first extranonce and tells the miner to search a
four-byte second extranonce:

```json
{"id":1,"result":[[["mining.set_difficulty","..."],["mining.notify","..."]],"01020304",4],"error":null}
```

The miner authorizes a worker label:

```json
{"id":2,"method":"mining.authorize","params":["qday.rig1","x"]}
```

The bridge accepts any short, nonempty worker label. It does not use that label
as a payment address and does not maintain pool accounts.

Before each job, the bridge sends the Stratum difficulty corresponding to the
QDAY network target:

```json
{"id":null,"method":"mining.set_difficulty","params":[12345.6789]}
```

Difficulty 1 uses the Sia Stratum target:

```text
00000000ffff0000000000000000000000000000000000000000000000000000
```

The floating-point difficulty is `difficulty_1_target / qday_target`. QDAY still
checks the reconstructed hash against the exact 256-bit target before a block
is submitted.

## Work notification

```json
{
  "id":null,
  "method":"mining.notify",
  "params":[
    "job-id",
    "<32-byte parent ID>",
    "<23-byte transaction prefix>",
    "<2-byte transaction suffix>",
    ["<left Merkle root>","<left Merkle root>"],
    "",
    "<compact target>",
    "<8-byte little-endian Unix time>",
    true
  ]
}
```

The fields are the Sia Stratum fields in their standard order: job ID,
`prevhash`, `coinb1`, `coinb2`, Merkle branch, unused version, informational
compact target, `ntime` and `clean_jobs`.

The bridge uses the final QDAY template transaction as Sia Stratum's arbitrary
transaction:

```text
transaction = coinb1 || extranonce1 || extranonce2 || coinb2
            = 23 bytes || 4 bytes || 4 bytes || 2 bytes
```

The result is a canonical 33-byte empty QDAY mining-work transaction. Its
eight-byte nonce is deliberately mutable and carries both extranonces. The
signed payout marker and all user transactions are earlier leaves in the same
commitment and never change.

Its leaf hash is:

```text
root = BLAKE2b-256(0x00 || transaction)
```

Each Merkle branch value is a left-side root. Fold them in the order supplied:

```text
root = BLAKE2b-256(0x01 || branch || root)
```

The final value is the QDAY block commitment. The miner's 80-byte work header
is:

```text
parentID[32] || nonceLE64 || timestampLE64 || commitment[32]
```

The winning condition is a BLAKE2b-256 header hash numerically less than or
equal to QDAY's 256-bit big-endian target. QDAY permits every uint64 nonce.

## Submission

The Sia submission has five string parameters:

```json
{
  "id":3,
  "method":"mining.submit",
  "params":[
    "qday.rig1",
    "job-id",
    "05060708",
    "<8-byte little-endian Unix time>",
    "<8-byte little-endian nonce>"
  ]
}
```

The bridge rejects unknown jobs, duplicate submissions, extranonces of the
wrong size, a changed timestamp, malformed fields and hashes above the exact
QDAY target. QDAY consensus performs the final timestamp check.

For a valid hash, the bridge reconstructs the final mining-work transaction,
commitment and complete block, inserts the submitted header nonce, then calls:

```http
POST /api/miner/submitblock
Authorization: Bearer <local api.token>
Content-Type: application/json

{"params":["<hex-encoded complete QDAY v2 block>"]}
```

The node validates the parent, proof of work, timestamp, payout, mandatory QDAY
marker, every mempool transaction and the commitment before relaying the block.

## Template extension

QDAY's mining template contains the normal transaction-aware fields plus:

```json
{
  "stratum": {
    "block":"<hex-encoded complete QDAY v2 block>",
    "coinbase1":"<46 hex characters>",
    "coinbase2":"<4 hex characters>",
    "extranonce1Size":4,
    "extranonce2Size":4,
    "merklebranch":["<64 hex characters>"]
  }
}
```

`merklebranch` proves every leaf to the left of the final transaction: the
parent-state/payout leaf, the mandatory marker when another transaction follows
it, and any earlier mempool transactions. The order is exactly the order needed
by Sia Stratum's rightmost-leaf fold.

At block 9,100 the final work transaction becomes a consensus requirement. The
bridge accepts only this format and stays idle before activation. The upgrade
does not change genesis or P2P identity.

## Other methods

The bridge returns an empty object for `mining.configure` and `true` for
`mining.extranonce.subscribe` and `mining.suggest_difficulty`. Suggested
difficulty does not replace the network target because every accepted result in
solo mode must be a valid QDAY block.
