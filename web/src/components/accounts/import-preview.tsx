import type { BatchPreview } from "@/lib/types"
import { loginProviderLabel } from "@/lib/login-provider"

export function ImportPreview({ preview }: { preview: BatchPreview }) {
  return (
    <section aria-label="账号导入预览" className="min-w-0 space-y-2">
      <p role="status" aria-live="polite" className="text-sm">
        共 {preview.total} 行账号 · 可导入 {preview.importable} · 重复 {preview.duplicates} · 错误 {preview.invalid}
      </p>
      <div className="max-h-52 overflow-auto rounded-md border" tabIndex={0} aria-label="导入校验明细，可滚动">
        <table className="w-full text-left text-xs">
          <caption className="sr-only">脱敏邮箱、登录方式和逐行校验结果</caption>
          <thead className="sticky top-0 bg-muted">
            <tr>
              <th scope="col" className="p-2 whitespace-nowrap">行号</th>
              <th scope="col" className="p-2">邮箱</th>
              <th scope="col" className="p-2 whitespace-nowrap">登录方式</th>
              <th scope="col" className="p-2">校验结果</th>
            </tr>
          </thead>
          <tbody>
            {preview.items.map((item) => (
              <tr key={item.line} className="border-t align-top">
                <td className="p-2">{item.line}</td>
                <td className="p-2 break-all">{item.email || "—"}</td>
                <td className="p-2 whitespace-nowrap">{item.login_provider ? loginProviderLabel(item.login_provider) : "—"}</td>
                <td className={`p-2 ${item.status === "invalid" ? "text-destructive" : "text-muted-foreground"}`}>
                  {item.message}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="text-xs text-muted-foreground">
        {preview.invalid > 0
          ? "请按行号修正错误后重新预览，本批尚未保存。"
          : preview.importable === 0
            ? "没有可导入的新账号，已有账号的凭据保持不变。"
            : "重复账号将跳过，已有凭据保持不变。确认导入时会再次校验。"}
      </p>
    </section>
  )
}
