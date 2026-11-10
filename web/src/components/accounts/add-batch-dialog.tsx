import { useRef, useState } from "react"
import { toast } from "sonner"
import { api } from "@/lib/api"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { type LoginProvider, loginProviderLabel } from "@/lib/login-provider"
import { MAX_ACCOUNT_IMPORT_BYTES, readAccountFile } from "@/lib/account-import"
import type { BatchPreview } from "@/lib/types"
import { ImportPreview } from "./import-preview"

function nextBatchName() {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, "0")
  return `批次-${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`
}

export function AddBatchDialog({ onSaved }: { onSaved: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState(nextBatchName)
  const [text, setText] = useState("")
  const [loginProvider, setLoginProvider] = useState<LoginProvider>("google")
  const [pending, setPending] = useState<"file" | "preview" | "save" | null>(null)
  const [preview, setPreview] = useState<BatchPreview | null>(null)
  const [error, setError] = useState("")
  const generation = useRef(0)

  function reset(v: boolean) {
    generation.current++
    setOpen(v)
    setText("")
    setPreview(null)
    setError("")
    setPending(null)
    if (v) {
      setName(nextBatchName())
      setLoginProvider("google")
    }
  }

  function openChange(v: boolean) {
    if (pending !== "save") reset(v)
  }

  function invalidatePreview() {
    generation.current++
    setPreview(null)
    setError("")
  }

  async function loadFile(file: File) {
    invalidatePreview()
    const request = generation.current
    setPending("file")
    try {
      const content = await readAccountFile(file)
      if (request === generation.current) setText(content)
    } catch (err) {
      if (request === generation.current) setError(err instanceof Error ? err.message : "文件读取失败")
    } finally {
      if (request === generation.current) setPending(null)
    }
  }

  async function onPreview() {
    invalidatePreview()
    const request = generation.current
    if (new TextEncoder().encode(text).length > MAX_ACCOUNT_IMPORT_BYTES) {
      setError("账号文本不能超过 1 MiB")
      return
    }
    setPending("preview")
    try {
      const result = await api.previewBatch({ name, text, login_provider: loginProvider })
      if (request === generation.current) setPreview(result)
    } catch (err) {
      if (request === generation.current) setError(err instanceof Error ? err.message : "预览失败")
    } finally {
      if (request === generation.current) setPending(null)
    }
  }

  async function onSubmit() {
    if (!preview || preview.invalid || !preview.importable || pending) return
    setPending("save")
    setError("")
    try {
      const res = await api.createBatch({ name, text, login_provider: loginProvider })
      if (res.errors?.length) toast.warning(`批次已创建，部分账号跳过：${res.errors.join("；")}`)
      else toast.success(`已创建 ${res.batch.name}，共 ${res.batch.total} 个账号`)
      reset(false)
      onSaved()
    } catch (err) {
      setPreview(null)
      setError(err instanceof Error ? err.message : "创建失败，请重新预览后重试")
    } finally {
      setPending(null)
    }
  }

  return (
    <Dialog open={open} onOpenChange={openChange}>
      <DialogTrigger asChild>
        <Button>导入邮箱账号</Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-2xl" showCloseButton={pending !== "save"}>
        <DialogHeader>
          <DialogTitle>导入已有邮箱账号到 Auto</DialogTitle>
          <DialogDescription>
            选择 TXT 文件或粘贴账号，先预览再确认。导入只保存到已连接的 Auto，不会自动启动登录。
          </DialogDescription>
        </DialogHeader>
        <fieldset disabled={pending !== null} className="grid min-w-0 gap-3">
          <div className="grid gap-2">
            <Label>两列／三列账号的登录方式</Label>
            <div className="grid grid-cols-2 gap-2">
              <Button type="button" aria-pressed={loginProvider === "google"} variant={loginProvider === "google" ? "default" : "outline"} onClick={() => { invalidatePreview(); setLoginProvider("google") }}>
                谷歌
              </Button>
              <Button type="button" aria-pressed={loginProvider === "microsoft"} variant={loginProvider === "microsoft" ? "default" : "outline"} onClick={() => { invalidatePreview(); setLoginProvider("microsoft") }}>
                微软
              </Button>
            </div>
            <p className="text-xs text-muted-foreground">两列／三列按「{loginProviderLabel(loginProvider)}」保存；六列自动识别为微软。每行一个账号。</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="batch-name">批次名</Label>
            <Input id="batch-name" value={name} onChange={(e) => { invalidatePreview(); setName(e.target.value) }} />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="batch-file">选择 TXT 文件</Label>
            <Input id="batch-file" type="file" accept=".txt,text/plain" aria-describedby="batch-file-help" onChange={(e) => {
              const file = e.target.files?.[0]
              e.target.value = ""
              if (file) void loadFile(file)
            }} />
            <p id="batch-file-help" className="text-xs text-muted-foreground">UTF-8 编码，最多 1 MiB、1000 个账号。选择文件会替换下方文本。</p>
          </div>
          <div className="grid gap-2">
            <Label htmlFor="batch-text">账号列表</Label>
            <Textarea
              id="batch-text"
              rows={5}
              spellCheck={false}
              autoComplete="off"
              value={text}
              onChange={(e) => { invalidatePreview(); setText(e.target.value) }}
              placeholder={
                loginProvider === "microsoft"
                  ? "user1@outlook.com----pass\nuser2@outlook.com----pass"
                  : "user1@example.com----pass----backup@example.com\nuser2@example.com----pass----backup@example.com"
              }
            />
            <p className="text-xs text-muted-foreground break-words">
              支持：邮箱----密码；邮箱----密码----辅助邮箱；微软六列：邮箱----密码----Client ID----Refresh Token----辅助邮箱----附加字段。第六列仅保留。
            </p>
          </div>
        </fieldset>
        {pending === "file" && <p role="status">正在读取文件…</p>}
        {pending === "preview" && <p role="status">正在校验账号与重复项…</p>}
        {error && <p role="alert" className="text-sm text-destructive break-words">{error}</p>}
        {preview && <ImportPreview preview={preview} />}
        <DialogFooter>
          <Button variant="outline" disabled={pending === "save"} onClick={() => openChange(false)}>
            取消
          </Button>
          <Button variant="outline" disabled={pending !== null || !text.trim()} onClick={onPreview}>
            {pending === "preview" ? "正在校验…" : "预览与校验"}
          </Button>
          <Button disabled={pending !== null || !preview || preview.invalid > 0 || preview.importable === 0} onClick={onSubmit}>
            {pending === "save" ? "正在导入…" : preview ? `确认导入 ${preview.importable} 个账号` : "确认导入"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
