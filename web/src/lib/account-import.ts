export const MAX_ACCOUNT_IMPORT_BYTES = 1 << 20

export async function readAccountFile(file: File): Promise<string> {
  if (!/\.txt$/i.test(file.name)) throw new Error("请选择 .txt 账号文件")
  if (!file.size) throw new Error("账号文件为空")
  if (file.size > MAX_ACCOUNT_IMPORT_BYTES) throw new Error("账号文件不能超过 1 MiB")
  try {
    return new TextDecoder("utf-8", { fatal: true }).decode(await file.arrayBuffer())
  } catch {
    throw new Error("无法读取账号文件，请确认文件为 UTF-8 编码")
  }
}
