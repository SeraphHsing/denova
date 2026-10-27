import type { ReactNode } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Empty, EmptyHeader, EmptyTitle } from '@/components/ui/empty'
import { cn } from '@/lib/utils'

export function ChartPanel({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return <Card size="sm"><CardHeader className="flex flex-wrap items-center justify-between gap-2"><CardTitle>{title}</CardTitle>{action}</CardHeader><CardContent>{children}</CardContent></Card>
}

export function ChartEmpty({ label }: { label: string }) {
  return <Empty className="py-8"><EmptyHeader><EmptyTitle>{label}</EmptyTitle></EmptyHeader></Empty>
}

export function MetricStrip({ items }: { items: Array<{ label: string; value: string; detail?: ReactNode }> }) {
  return <dl className={cn('grid grid-cols-2 gap-x-4 gap-y-3', items.length > 4 ? 'sm:grid-cols-3 xl:grid-cols-6' : 'sm:grid-cols-4')}>{items.map(item => (
    <div key={item.label} className="flex min-w-0 flex-col gap-1">
      <dt className="text-xs text-muted-foreground">{item.label}</dt>
      <dd className="text-xl font-semibold tracking-tight tabular-nums">{item.value}</dd>
      {item.detail && <dd className="text-xs text-muted-foreground">{item.detail}</dd>}
    </div>
  ))}</dl>
}
