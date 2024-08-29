## Selfdestruct

These files exemplify a selfdestruct to the `0`-address. 

## Execution

Running it yields the post-alloc:

```
$ go run . t8n --state.fork=Shanghai --input.alloc=testdata/2/alloc.json --input.txs=testdata/2/txs.json --input.env=testdata/2/env.json --output.alloc=stdout 2>/dev/null
{
  "alloc": {
    "0x0000000000000000000000000000000000000000": {
      "balance": "0xde0b6b3a76586a0"
    },
    "0x20922f242a32cbb2d4cd75e397694cdbfac1242a": {
      "balance": "0xde0b6b3a183ed4f",
      "nonce": "0x1"
    }
  }
}
```