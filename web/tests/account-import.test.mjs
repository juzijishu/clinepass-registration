import assert from "node:assert/strict"
import test from "node:test"
import { readAccountFile } from "../src/lib/account-import.ts"

test("TXT import reads UTF-8 with BOM and CRLF without changing credentials", async () => {
  const file = new File(["\ufeffone@example.test----synthetic-password\r\ntwo@example.test----pw\r\n"], "accounts.TXT")
  assert.equal(await readAccountFile(file), "one@example.test----synthetic-password\r\ntwo@example.test----pw\r\n")
})

test("empty, oversized, non-TXT and invalid UTF-8 files fail without echoing content", async () => {
  for (const file of [
    new File([], "empty.txt"),
    new File([new Uint8Array((1 << 20) + 1)], "large.txt"),
    new File(["synthetic-private"], "accounts.csv"),
    new File([new Uint8Array([0xff, 0xfe, 0x41, 0x00])], "utf16.txt"),
  ]) {
    await assert.rejects(readAccountFile(file), (error) => {
      assert.doesNotMatch(error.message, /synthetic-private/)
      return true
    })
  }
})
