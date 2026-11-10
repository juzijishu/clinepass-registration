import { useEffect, useMemo, useRef, useState } from "react"
import { Link, useParams } from "react-router-dom"
import { toast } from "sonner"
import { api } from "@/lib/api"
import type { Account, Batch, BatchRun, Job } from "@/lib/types"
import { Check } from "lucide-react"
import { AccountTable } from "@/components/accounts/account-table"
import { AutoPayDialog } from "@/components/accounts/auto-pay-dialog"
import { AutoImport } from "@/components/accounts/auto-import"
import { AutoVerificationStatus, useAutoVerification } from "@/components/accounts/auto-verification"
import { DetailDialog } from "@/components/accounts/detail-dialog"
import { JobLogPanel } from "@/components/accounts/job-log-panel"
import { Button } from "@/components/ui/button"
import { batchStatus, radarDeniedCount, waitingCount } from "@/lib/batch-ui"
import { batchRunCounts, latestJobsByAccount, logsFromJobs } from "@/lib/job-log"
import { downloadBase64, xlsxMime } from "@/lib/download"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

export function BatchDetailPage() {
  const { id } = useParams<{ id: string }>()
  const [batch, setBatch] = useState<Batch | null>(null)
  const [runState, setRunState] = useState<BatchRun | null>(null)
  const [accounts, setAccounts] = useState<Account[]>([])
  const [detail, setDetail] = useState<Account | null>(null)
  const [remove, setRemove] = useState<Account | null>(null)
  const [removeRadar, setRemoveRadar] = useState(false)
  const [removingRadar, setRemovingRadar] = useState(false)
  const [payAsk, setPayAsk] = useState<null | { mode: "login" | "refresh"; account?: Account }>(null)
  const [payPending, setPayPending] = useState(false)
  const [subscriptionAsk, setSubscriptionAsk] = useState<Account | null>(null)
  const [subscriptionPending, setSubscriptionPending] = useState(false)
  const [subscriptionError, setSubscriptionError] = useState("")
  const [batchSubscriptionAsk, setBatchSubscriptionAsk] = useState(false)
  const [batchSubscriptionPending, setBatchSubscriptionPending] = useState(false)
  const [batchSubscriptionError, setBatchSubscriptionError] = useState("")
  const subscriptionConfirm = useRef<HTMLButtonElement>(null)
  const subscriptionReturn = useRef("")
  const [jobs, setJobs] = useState<Job[]>([])
  const [now, setNow] = useState(Date.now())
  const [logFilter, setLogFilter] = useState("")
  const [loadError, setLoadError] = useState("")
  const routeRef = useRef(id)
  routeRef.current = id
  const revisionRef = useRef(0)
  const reloadRef = useRef(0)
  const radarCount = radarDeniedCount(accounts)
  const latestJobs = useMemo(() => latestJobsByAccount(jobs), [jobs])
  const logs = useMemo(() => logsFromJobs(jobs), [jobs])
  const counts = useMemo(() => batchRunCounts(accounts, latestJobs), [accounts, latestJobs])
  const verification = useAutoVerification(() => { void reload().catch(() => {}) })

  useEffect(() => {
    if (subscriptionAsk && subscriptionError && !subscriptionPending) subscriptionConfirm.current?.focus()
  }, [subscriptionAsk, subscriptionError, subscriptionPending])

  async function reload() {
    if (!id) return
    const request = ++reloadRef.current
    const revision = revisionRef.current
    let data
    try {
      data = await api.batch(id)
      if (routeRef.current !== id || request !== reloadRef.current || revision !== revisionRef.current) return
      setLoadError("")
    } catch (err) {
      if (routeRef.current !== id || request !== reloadRef.current || revision !== revisionRef.current) return
      setLoadError(err instanceof Error ? err.message : "无法读取远程批次")
      throw err
    }
    setBatch(data.batch)
    setRunState(data.run || null)
    setAccounts(data.accounts)
    setDetail((cur) => (cur ? data.accounts.find((a) => a.id === cur.id) || null : null))
    setLogFilter((cur) => (cur && !data.accounts.some((a) => a.id === cur) ? "" : cur))
    return data
  }

  useEffect(() => {
    let cancelled = false
    let timer = 0
    let initialized = false
    setBatch(null)
    setRunState(null)
    setAccounts([])
    setJobs([])
    setLogFilter("")
    setDetail(null)
    setPayAsk(null)
    setPayPending(false)
    setSubscriptionAsk(null)
    setSubscriptionPending(false)
    setSubscriptionError("")
    setRemove(null)
    setLoadError("")
    revisionRef.current++
    const tick = async () => {
      const revision = revisionRef.current
      const request = ++reloadRef.current
      try {
        const [data, allJobs] = await Promise.all([api.batch(id!), api.jobs()])
        if (cancelled || routeRef.current !== id || revision !== revisionRef.current || request !== reloadRef.current) return
        const ids = new Set(data.accounts.map((a) => a.id))
        const mine = allJobs.filter((j) => ids.has(j.account_id))
        // Each response is a full snapshot. Replacing it retains repeat-count updates.
        setJobs(mine)
        setBatch(data.batch)
        setRunState(data.run || null)
        setAccounts(data.accounts)
        setLoadError("")
        setDetail((current) => current ? data.accounts.find((a) => a.id === current.id) || null : null)
        const firstLoad = !initialized
        setLogFilter((current) => {
          if (current && ids.has(current)) return current
          if (firstLoad) return mine.find((j) => j.status === "running")?.account_id || data.accounts.find((a) => a.status === "failed")?.id || data.accounts[0]?.id || ""
          return ""
        })
        initialized = true
      } catch (e) {
        if (!cancelled && routeRef.current === id && revision === revisionRef.current && request === reloadRef.current) setLoadError(e instanceof Error ? e.message : "加载失败")
      } finally {
        if (!cancelled) timer = window.setTimeout(() => void tick(), 1500)
      }
    }
    if (id) void tick()
    const clock = window.setInterval(() => setNow(Date.now()), 1000)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
      window.clearInterval(clock)
      revisionRef.current++
    }
  }, [id])

  function followJobs(nextJobs: Job[]) {
    revisionRef.current++
    const accountIDs = new Set(nextJobs.map((job) => job.account_id))
    setJobs((current) => [...current.filter((job) => !accountIDs.has(job.account_id)), ...nextJobs])
    setAccounts((current) => current.map((account) => accountIDs.has(account.id) ? { ...account, status: "queued", last_error: "" } : account))
    setLogFilter((current) => current || nextJobs[0]?.account_id || "")
  }

  function askSubscription(account: Account) {
    subscriptionReturn.current = account.id
    setSubscriptionError("")
    setSubscriptionAsk(account)
  }

  async function startSubscription() {
    if (!id || !subscriptionAsk || subscriptionPending) return
    const requestBatch = id
    const account = subscriptionAsk
    setSubscriptionPending(true)
    setSubscriptionError("")
    revisionRef.current++
    try {
      const job = await api.subscriptionAccount(account.id)
      if (routeRef.current !== requestBatch) return
      followJobs([job])
      setLogFilter(account.id)
      setSubscriptionAsk(null)
      toast.success("订阅后处理已排队")
    } catch (err) {
      if (routeRef.current === requestBatch) setSubscriptionError(err instanceof Error ? err.message : "启动失败")
    } finally {
      if (routeRef.current === requestBatch) setSubscriptionPending(false)
    }
  }

  async function startBatchSubscription() {
    if (!id || batchSubscriptionPending) return
    const requestBatch = id
    setBatchSubscriptionPending(true)
    setBatchSubscriptionError("")
    revisionRef.current++
    try {
      const jobs = await api.subscriptionBatch(requestBatch)
      if (routeRef.current !== requestBatch) return
      if (!jobs.length) {
        toast.message("没有需要入池的账号，已支付且未入池的账号才会排队")
      } else {
        followJobs(jobs)
        toast.success(`已排队关闭续订并入池，共 ${jobs.length} 个账号`)
      }
      setBatchSubscriptionAsk(false)
    } catch (err) {
      if (routeRef.current === requestBatch) setBatchSubscriptionError(err instanceof Error ? err.message : "启动失败")
    } finally {
      if (routeRef.current === requestBatch) setBatchSubscriptionPending(false)
    }
  }

  async function startWithAutoPay(autoPay: boolean) {
    if (!id || !payAsk) return
    const requestBatch = id
    revisionRef.current++
    setPayPending(true)
    try {
      if (payAsk.account) {
        setLogFilter(payAsk.account.id)
        const job =
          payAsk.mode === "refresh"
            ? await api.refreshAccount(payAsk.account.id, autoPay)
            : await api.loginAccount(payAsk.account.id, autoPay)
        if (routeRef.current !== requestBatch) return
        followJobs([job])
      } else if (payAsk.mode === "refresh") {
        const jobs = await api.refreshBatch(id, autoPay)
        if (routeRef.current !== requestBatch) return
        if (!jobs?.length) toast.message("这批还没有登录成功的账号，请先生成支付链接")
        else {
          toast.success(`开始刷新支付链接，共 ${jobs.length} 个账号${autoPay ? "，并自动支付" : ""}`)
          followJobs(jobs)
        }
      } else {
        const jobs = await api.loginBatch(id, autoPay)
        if (routeRef.current !== requestBatch) return
        if (!jobs?.length) toast.message("这批账号都已经登录过了")
        else {
          toast.success(`开始登录并生成支付链接，共 ${jobs.length} 个账号${autoPay ? "，并自动支付" : ""}`)
          followJobs(jobs)
        }
      }
      setPayAsk(null)
    } catch (e) {
      if (routeRef.current === requestBatch) toast.error(e instanceof Error ? e.message : "启动失败")
    } finally {
      if (routeRef.current === requestBatch) setPayPending(false)
    }
  }

  async function confirmRemove() {
    if (!remove) return
    try {
      await api.deleteAutoAccount(remove.id)
      setRemove(null)
      toast.success("已从 Auto 删除")
      await reload()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "删除失败")
    }
  }

  async function confirmRemoveRadar() {
    if (!id) return
    setRemovingRadar(true)
    try {
      const res = await api.deleteRadarDenied(id)
      setRemoveRadar(false)
      if (res.deleted === 0) {
        toast.message("这批没有 AuthKit Radar 拦截账户")
      } else {
        toast.success(`已删除 ${res.deleted} 个 AuthKit Radar 拦截账户`)
      }
      await reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "删除失败")
    } finally {
      setRemovingRadar(false)
    }
  }

  async function exportPay() {
    if (!id) return
    try {
      const res = await api.dispatchBatch(id)
      downloadBase64(res.filename || `${batch?.name || "batch"}-支付链接.xlsx`, res.xlsx, xlsxMime)
      toast.success(`已下载 ${res.count} 条，含账号和密码`)
      await reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "下载失败")
    }
  }

  async function markPaid() {
    if (!id) return
    try {
      await verification.start(id)
      toast.success("Auto 正在检查订阅，确认已支付后可导入账号池")
      await reload()
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "标记失败")
    }
  }

  const total = batch?.total ?? 0
  const paid = batch?.paid_count ?? 0
  const inPool = batch?.in_pool ?? 0
  const failed = batch?.failed ?? 0
  const ready = batch?.ready ?? 0
  const unpaidPay = batch?.unpaid_pay_count ?? 0
  const unpaidCookie = batch?.unpaid_cookie_count ?? 0
  const waiting = batch ? waitingCount(batch) : 0
  const finished = [total > 0, total > 0 && ready + failed >= total, total > 0 && paid >= total, paid > 0 && inPool >= paid]
  const currentStep = finished.findIndex((done) => !done)
  const isCurrent = (index: number) => currentStep === index

  const steps = [
    {
      no: 1,
      title: "账号就绪",
      detail: total ? `本批 ${total} 个邮箱账号已在队列里` : "正在读取批次…",
      done: finished[0],
      current: isCurrent(0),
      actions: null,
      extra: null,
    },
    {
      no: 2,
      title: "登录注册",
      detail: total
        ? `已出结果 ${Math.min(total, ready + failed)}/${total}${counts.running ? ` · ${counts.running} 进行中` : ""}${counts.queued ? ` · ${counts.queued} 排队` : ""}${failed ? ` · ${failed} 失败` : ""}`
        : "等待批次数据",
      done: finished[1],
      current: isCurrent(1),
      actions: <>
        <Button size="sm" variant={isCurrent(1) ? "default" : "outline"} disabled={!batch || !(waiting > 0 || failed > 0)} onClick={() => setPayAsk({ mode: "login" })}>
          登录并生成支付链接
        </Button>
        <Button size="sm" variant="ghost" disabled={!unpaidCookie || verification.busy} onClick={() => setPayAsk({ mode: "refresh" })}>
          刷新过期链接
        </Button>
      </>,
      extra: null,
    },
    {
      no: 3,
      title: "确认支付",
      detail: total
        ? `已支付 ${paid}/${total}${unpaidPay ? ` · ${unpaidPay} 个待付款链接` : ""}`
        : "等待批次数据",
      done: finished[2],
      current: isCurrent(2),
      actions: <>
        <Button size="sm" variant={isCurrent(2) && unpaidPay > 0 ? "default" : "outline"} disabled={!unpaidPay} onClick={exportPay}>
          下载 Excel
        </Button>
        <Button size="sm" variant={isCurrent(2) && unpaidPay === 0 ? "default" : "outline"} disabled={!unpaidCookie || verification.busy} onClick={markPaid}>
          {verification.busy ? "正在检查支付状态…" : "检查支付状态"}
        </Button>
      </>,
      extra: <AutoVerificationStatus status={verification.status} error={verification.error} />,
    },
    {
      no: 4,
      title: "关闭续订并入池",
      detail: paid ? `已支付 ${paid} 个 · 已入池 ${inPool} 个` : "支付完成后才能入池",
      done: finished[3],
      current: isCurrent(3),
      actions: <Button size="sm" variant={isCurrent(3) ? "default" : "outline"} disabled={!paid} onClick={() => { setBatchSubscriptionError(""); setBatchSubscriptionAsk(true) }}>
        关闭续订并入池{paid ? `（${Math.max(0, paid - inPool)} 个待处理）` : ""}
      </Button>,
      extra: null,
    },
    {
      no: 5,
      title: "导入主程序",
      detail: "把已支付并提取完成的账号加入主程序账号池，已有账号会更新。",
      done: false,
      current: currentStep === -1,
      actions: null,
      extra: <AutoImport key={id} batchId={id} disabled={!batch || !!loadError || verification.busy} flat />,
    },
  ]

  return (
    <div className="min-w-0 space-y-4">
      <div>
        <Button variant="ghost" size="sm" asChild className="-ml-2 mb-1">
          <Link to="/automation">← 返回批次列表</Link>
        </Button>
        <h1 className="text-2xl font-semibold tracking-tight">{batch?.name || "批次"}</h1>
        {batch ? (
          <div className="mt-2 flex flex-wrap items-center gap-2">
            <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${batchStatus(batch).color}`}>
              {batchStatus(batch).label}
            </span>
            <span className="text-sm text-muted-foreground">
              {batch.total} 个账号
              {batch.sms_spent ? ` · 接码花费 ${batch.sms_spent.toFixed(2)}` : ""}
            </span>
            {runState && runState.state !== "idle" ? (
              <span className={`inline-flex rounded-full px-2 py-0.5 text-xs font-medium ${runState.state === "done" ? "bg-emerald-600 text-white" : "bg-sky-600 text-white"}`}>
                {runState.state === "done" ? "整批已跑完" : `运行中 ${runState.running} 进行 ${runState.queued} 排队`}
              </span>
            ) : null}
          </div>
        ) : null}
      </div>

      {loadError ? <div role="alert" className="space-y-3 rounded-xl border border-destructive/25 bg-destructive/5 p-4"><p className="text-sm">{loadError}</p><div className="flex gap-2"><Button variant="outline" size="sm" onClick={() => void reload().catch(() => {})}>重新加载</Button><Button variant="outline" size="sm" asChild><Link to="/settings?tab=connection">配置 Auto 连接</Link></Button></div></div> : null}

      <section className="overflow-hidden rounded-2xl border bg-card" aria-label="注册流程">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
          <div>
            <h2 className="text-sm font-semibold">注册流程</h2>
            <p className="mt-0.5 text-xs text-muted-foreground">按顺序往下走，当前步骤会高亮。</p>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-xs">
            {counts.running ? <span className="rounded-full bg-sky-500/10 px-2 py-0.5 font-medium text-sky-700 dark:text-sky-300">{counts.running} 进行中</span> : null}
            {counts.queued ? <span className="rounded-full bg-muted px-2 py-0.5 font-medium text-muted-foreground">{counts.queued} 排队</span> : null}
          </div>
        </div>
        <ol className="divide-y">
          {steps.map((step) => (
            <li key={step.no} className={step.current ? "bg-primary/[0.04]" : ""}>
              <div className="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="flex min-w-0 items-start gap-3">
                  <span className={`mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold tabular-nums ${step.done ? "bg-emerald-600 text-white" : step.current ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground"}`}>
                    {step.done ? <Check className="size-3.5" /> : step.no}
                  </span>
                  <div className="min-w-0 space-y-0.5">
                    <p className="text-sm font-medium">{step.title}</p>
                    <p className="text-xs leading-5 text-muted-foreground">{step.detail}</p>
                  </div>
                </div>
                {step.actions ? <div className="flex shrink-0 flex-wrap gap-2 sm:pl-3">{step.actions}</div> : null}
              </div>
              {step.extra ? <div className="px-4 pb-3 sm:pl-[3.25rem]">{step.extra}</div> : null}
            </li>
          ))}
        </ol>
      </section>

      {radarCount > 0 ? (
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <span>{radarCount} 个账号被 AuthKit Radar 拦截，无法继续。</span>
          <Button variant="ghost" size="sm" className="h-7 px-2 text-destructive hover:text-destructive" disabled={removingRadar} onClick={() => setRemoveRadar(true)}>
            删除这些账号
          </Button>
        </div>
      ) : null}

      <div className="grid min-w-0 gap-4 lg:h-[clamp(36rem,calc(100dvh-20rem),44rem)] lg:grid-cols-[minmax(0,1fr)_minmax(0,1.08fr)]">
        <AccountTable
          accounts={accounts}
          jobs={latestJobs}
          now={now}
          onLogin={(a) => setPayAsk({ mode: "login", account: a })}
          onRefresh={(a) => setPayAsk({ mode: "refresh", account: a })}
          onSubscription={askSubscription}
          onDetail={setDetail}
          onRemove={setRemove}
          selectedId={logFilter}
          onSelect={(a) => setLogFilter(a.id)}
        />

        <JobLogPanel
          logs={logs}
          accounts={accounts}
          jobs={latestJobs}
          now={now}
          filterId={logFilter}
          onFilter={setLogFilter}
          onRetry={(a) => latestJobs[a.id]?.kind === "subscription" ? askSubscription(a) : setPayAsk({ mode: "login", account: a })}
        />
      </div>

      <AlertDialog open={batchSubscriptionAsk} onOpenChange={(open) => { if (!open && !batchSubscriptionPending) setBatchSubscriptionAsk(false) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>批量关闭续订并入池</AlertDialogTitle>
            <AlertDialogDescription className="break-words [overflow-wrap:anywhere]">
              将对本批已支付、尚未入池的账号依次确认有效订阅、关闭自动续订、创建并保存 Key，再发送到设置中的 clinebypass 号池。已入池的账号会跳过；Key 创建结果不明的账号不入队，需要人工核对。
            </AlertDialogDescription>
          </AlertDialogHeader>
          {batchSubscriptionError ? <p role="alert" className="break-words text-sm text-destructive">{batchSubscriptionError}</p> : null}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={batchSubscriptionPending}>取消</AlertDialogCancel>
            <AlertDialogAction disabled={batchSubscriptionPending} onClick={(event) => { event.preventDefault(); void startBatchSubscription() }}>{batchSubscriptionPending ? "排队中…" : "确认批量入池"}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={!!subscriptionAsk} onOpenChange={(open) => { if (!open && !subscriptionPending) setSubscriptionAsk(null) }}>
        <AlertDialogContent onCloseAutoFocus={(event) => {
          event.preventDefault()
          document.getElementById(`account-actions-${subscriptionReturn.current}`)?.focus()
        }}>
          <AlertDialogHeader>
            <AlertDialogTitle>关闭自动续订并入池</AlertDialogTitle>
            <AlertDialogDescription className="break-words [overflow-wrap:anywhere]">
              将使用 {subscriptionAsk?.email} 已保存的授权会话，确认有效订阅，关闭自动续订并复查当前权益，再创建并保存 Key、发送到设置中的 clinebypass 号池。入池失败可复用已保存的 Key 重试；创建结果不明时会停止等待人工核对。
            </AlertDialogDescription>
          </AlertDialogHeader>
          {subscriptionError ? <p role="alert" className="break-words text-sm text-destructive">{subscriptionError}</p> : null}
          <AlertDialogFooter>
            <AlertDialogCancel disabled={subscriptionPending}>取消</AlertDialogCancel>
            <AlertDialogAction ref={subscriptionConfirm} disabled={subscriptionPending} onClick={(event) => { event.preventDefault(); void startSubscription() }}>{subscriptionPending ? "排队中…" : "确认关闭续订并入池"}</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AutoPayDialog
        open={!!payAsk}
        title={payAsk?.mode === "refresh" ? "刷新支付链接" : "登录并生成支付链接"}
        description={
          payAsk?.mode === "refresh"
            ? "用已有 Cookie 重新抽出支付链接。勾选自动支付后会立刻用 AmzKeys 虚拟卡去付 Stripe。"
            : "登录成功后抽出支付链接。勾选自动支付后不再下载 Excel，会开虚拟卡填 Stripe。"
        }
        pending={payPending}
        onOpenChange={(v) => !v && !payPending && setPayAsk(null)}
        onConfirm={startWithAutoPay}
      />

      <DetailDialog account={detail} source="auto" open={!!detail} onOpenChange={(v) => !v && setDetail(null)} />

      <AlertDialog open={!!remove} onOpenChange={(v) => !v && setRemove(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除账号</AlertDialogTitle>
            <AlertDialogDescription>从 Auto 删除 {remove?.email}？已经导入主程序的账号会保留。</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>取消</AlertDialogCancel>
            <AlertDialogAction variant="destructive" onClick={confirmRemove}>
              删除
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog open={removeRadar} onOpenChange={(v) => !v && !removingRadar && setRemoveRadar(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>删除 AuthKit Radar 拦截账户</AlertDialogTitle>
            <AlertDialogDescription>
              将删除本批 {radarCount} 个因 AuthKit Radar 拦截（policy_denied）失败的账户，其它账户不受影响。
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={removingRadar}>取消</AlertDialogCancel>
            <AlertDialogAction variant="destructive" disabled={removingRadar} onClick={confirmRemoveRadar}>
              {removingRadar ? "删除中…" : "确认删除"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
