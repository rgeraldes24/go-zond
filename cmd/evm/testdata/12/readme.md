## Test 1559 balance + gasCap

This test contains an EIP-1559 consensus issue which happened on Ropsten, where
`gzond` did not properly account for the value transfer while doing the check on `max_fee_per_gas * gas_limit`.

Before the issue was fixed, this invocation allowed the transaction to pass into a block:
```
$ go run . t8n --state.fork=Shanghai --input.alloc=testdata/12/alloc.json --input.txs=testdata/12/txs.json --input.env=testdata/12/env.json --output.alloc=stdout --output.result=stdout
```

With the fix applied, the result is: 
```
go run . t8n --state.fork=Shanghai --input.alloc=testdata/12/alloc.json --input.txs=testdata/12/txs.json --input.env=testdata/12/env.json --output.alloc=stdout --output.result=stdout
INFO [08-29|13:58:34.361] rejected tx                              index=0 hash=dd9b2b..31143a from=0x20922F242A32cBb2d4CD75e397694cDBfac1242a error="insufficient funds for gas * price + value: address 0x20922F242A32cBb2d4CD75e397694cDBfac1242a have 84000000 want 84000032"
INFO [08-29|13:58:34.361] Trie dumping started                     root=e13736..a39dbb
INFO [08-29|13:58:34.361] Trie dumping complete                    accounts=1 elapsed="23.709µs"
{
  "alloc": {
    "0x20922f242a32cbb2d4cd75e397694cdbfac1242a": {
      "balance": "0x501bd00"
    }
  },
  "result": {
    "stateRoot": "0xe1373654d7b14379003e90d1547fac4a29e78bf044ceef9b77e9cdc7f2a39dbb",
    "txRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
    "receiptsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
    "logsHash": "0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347",
    "logsBloom": "0x00000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000",
    "receipts": [],
    "rejected": [
      {
        "index": 0,
        "error": "insufficient funds for gas * price + value: address 0x20922F242A32cBb2d4CD75e397694cDBfac1242a have 84000000 want 84000032"
      }
    ],
    "gasUsed": "0x0",
    "currentBaseFee": "0x20",
    "withdrawalsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
  }
}
```

The transaction is rejected. 