import { useState, type FormEvent } from "react"
import { toast } from "sonner"
import { api } from "@/lib/api"
import type { RegistrationSettings } from "@/lib/types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function SubscriptionSettings({ value, onChange, loaded }: {
  value: RegistrationSettings | null
  onChange: (value: RegistrationSettings) => void
  loaded: boolean
}) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState("")
  const passwordConfigured = value?.target_admin_password.includes("********")

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!value || !loaded || pending) return
    setPending(true)
    setError("")
    try {
      const cfg = await api.saveAutoConfig({ registration: {
        ...value,
        target_url: value.target_url.trim(),
        target_client_key_id: value.target_client_key_id.trim(),
      } })
      onChange(cfg.registration)
      toast.success("订阅与入池设置已保存")
    } catch (err) {
      setError(err instanceof Error ? err.message : "保存失败")
    } finally {
      setPending(false)
    }
  }

  return <Card>
    <CardHeader>
      <CardTitle>订阅与入池</CardTitle>
      <CardDescription>已有授权会话的账号可在批次详情中执行「关闭续订并入池」。完成后会回读目标号池确认 Key 和客户端归属。</CardDescription>
    </CardHeader>
    <CardContent>
      <form onSubmit={save} className="space-y-4">
        <fieldset disabled={!loaded || !value || pending} className="min-w-0 space-y-4">
          <div className="space-y-2">
            <Label htmlFor="subscription-target-url">clinebypass 地址</Label>
            <Input id="subscription-target-url" type="url" required value={value?.target_url || ""} onChange={event => value && onChange({ ...value, target_url: event.target.value })} placeholder="http://127.0.0.1:8080" aria-describedby="subscription-target-help" />
            <p id="subscription-target-help" className="text-xs leading-5 text-muted-foreground">从 Auto 所在服务器访问此地址。使用 HTTPS，本机回环地址可用 HTTP；填服务根地址。</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="subscription-target-password">号池管理员密码</Label>
            <Input id="subscription-target-password" type="password" autoComplete="new-password" required={!passwordConfigured} value={passwordConfigured ? "" : value?.target_admin_password || ""} onChange={event => value && onChange({ ...value, target_admin_password: event.target.value || (passwordConfigured ? value.target_admin_password : "") })} placeholder={passwordConfigured ? "已保存，留空保留" : "填写目标号池的管理员密码"} aria-describedby="subscription-password-help" />
            <p id="subscription-password-help" className="text-xs text-muted-foreground">{passwordConfigured ? "管理员密码已保存，提交时保留原值。" : "使用目标号池独立的管理员密码。"}</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="subscription-target-owner">客户端密钥归属 ID</Label>
            <Input id="subscription-target-owner" required value={value?.target_client_key_id || ""} onChange={event => value && onChange({ ...value, target_client_key_id: event.target.value })} placeholder="legacy 或 ck_…" aria-describedby="subscription-owner-help" />
            <p id="subscription-owner-help" className="text-xs leading-5 text-muted-foreground">填写目标号池已有的客户端密钥 ID。入池时会校验该归属存在。</p>
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <Label htmlFor="registration-enabled">自动串完整条流程</Label>
              <p className="text-xs leading-5 text-muted-foreground">开启后，账号登录成功且已支付会立刻排队关闭续订并入池，不用再逐个点。</p>
            </div>
            <Switch id="registration-enabled" checked={!!value?.enabled} onCheckedChange={(checked) => value && onChange({ ...value, enabled: checked })} />
          </div>
          <div className="flex items-start justify-between gap-4">
            <div className="space-y-1">
              <Label htmlFor="manual-sms">人工接码</Label>
              <p className="text-xs leading-5 text-muted-foreground">开启后不调用 HeroSMS：浏览器停在手机号输入页等你手动填号、收码并提交，完成后自动继续；需要可见的浏览器窗口。</p>
            </div>
            <Switch id="manual-sms" checked={!!value?.manual_sms} onCheckedChange={(checked) => value && onChange({ ...value, manual_sms: checked })} />
          </div>
          {value?.manual_sms ? (
            <div className="space-y-2">
              <Label htmlFor="manual-wait-seconds">人工接码等待（秒）</Label>
              <Input id="manual-wait-seconds" type="number" min={60} max={7200} value={value.manual_wait_seconds ?? 1800} onChange={event => onChange({ ...value, manual_wait_seconds: Number(event.target.value) })} aria-describedby="manual-wait-help" />
              <p id="manual-wait-help" className="text-xs text-muted-foreground">停在手机号输入页等你操作的最长时间，60–7200 秒，默认 1800 秒。超时按失败处理。</p>
            </div>
          ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="sms-max-attempts">单账号取号次数</Label>
              <Input id="sms-max-attempts" type="number" min={1} max={20} value={value?.sms_max_attempts ?? 3} onChange={event => value && onChange({ ...value, sms_max_attempts: Number(event.target.value) })} aria-describedby="sms-attempts-help" />
              <p id="sms-attempts-help" className="text-xs text-muted-foreground">一个账号最多取几次号，1–20。</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="sms-wait-seconds">等码超时（秒）</Label>
              <Input id="sms-wait-seconds" type="number" min={30} max={600} value={value?.sms_wait_seconds ?? 120} onChange={event => value && onChange({ ...value, sms_wait_seconds: Number(event.target.value) })} aria-describedby="sms-wait-help" />
              <p id="sms-wait-help" className="text-xs text-muted-foreground">等待验证码的最长时间，30–600 秒。</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="sms-account-budget">单账号预算</Label>
              <Input id="sms-account-budget" type="number" min={0} step="0.01" value={value?.sms_account_budget ?? 0} onChange={event => value && onChange({ ...value, sms_account_budget: Number(event.target.value) })} aria-describedby="sms-budget-help" />
              <p id="sms-budget-help" className="text-xs text-muted-foreground">0 表示不限制。</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="sms-batch-budget">整批预算</Label>
              <Input id="sms-batch-budget" type="number" min={0} step="0.01" value={value?.sms_batch_budget ?? 0} onChange={event => value && onChange({ ...value, sms_batch_budget: Number(event.target.value) })} aria-describedby="sms-batch-budget-help" />
              <p id="sms-batch-budget-help" className="text-xs text-muted-foreground">同一批次累计取号花费上限，0 表示不限制。</p>
            </div>
          </div>
          )}
          <p className="rounded-lg bg-muted/40 p-3 text-sm leading-6 text-muted-foreground">{value?.manual_sms ? "人工接码不取号、不消耗接码预算；等待期间浏览器和分阶段网络保持不动，登录完成后继续提取支付链接。" : "预算不足一次取号时不启动登录；取号实际花费会记到账号与批次上。"}</p>
          <p className="rounded-lg bg-muted/40 p-3 text-sm leading-6 text-muted-foreground">执行顺序：确认订阅 → 关闭续订并复查 → 创建并保存 Key → 追加到号池并回读。后处理全程直连；创建结果不明时停止，已保存的 Key 可用于入池重试。</p>
          <Button type="submit">{pending ? "保存中…" : "保存订阅与入池设置"}</Button>
        </fieldset>
        {error ? <p role="alert" className="break-words text-sm text-destructive">{error}</p> : null}
      </form>
    </CardContent>
  </Card>
}
