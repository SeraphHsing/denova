import { useTranslation } from 'react-i18next'
import { Bar, BarChart, CartesianGrid, Cell, ReferenceLine, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { Progress } from '@/components/ui/progress'
import { ChartEmpty, ChartPanel, MetricStrip } from './StatisticsCharts'
import { StatisticsTable } from './StatisticsTable'
import type { StatisticsReport } from './api'

export function WritingStatistics({ data }: { data: StatisticsReport['writing'] }) {
  const { t, i18n } = useTranslation()
  const number = (value: number) => new Intl.NumberFormat(i18n.language).format(value)
  const words = data.projects.reduce((sum, item) => sum + item.words, 0)
  const chapters = data.projects.reduce((sum, item) => sum + item.chapters, 0)
  const confirmed = data.projects.reduce((sum, item) => sum + item.confirmed, 0)
  const observed = data.days.filter(item => item.delta !== null)
  const delta = observed.reduce((sum, item) => sum + (item.delta ?? 0), 0)

  return <div className="flex flex-col gap-4">
    <MetricStrip items={[
      { label: t('statistics.manuscript'), value: number(words) },
      { label: t('statistics.netChange'), value: observed.length ? `${delta > 0 ? '+' : ''}${number(delta)}` : '—' },
      { label: t('statistics.chapters'), value: number(chapters) },
      { label: t('statistics.confirmed'), value: `${number(confirmed)} / ${number(chapters)}`, detail: chapters > 0 && <Progress value={100 * confirmed / chapters} aria-label={t('statistics.chapterProgress')} /> },
    ]} />
    <ChartPanel title={t('statistics.writingTrend')}>
      {observed.length ? <ChartContainer config={{ delta: { label: t('statistics.netChange'), color: 'var(--nova-success)' } }} className="h-48 w-full aspect-auto">
        <BarChart accessibilityLayer data={data.days} margin={{ top: 8, right: 8, left: 0 }}>
          <CartesianGrid vertical={false} />
          <XAxis dataKey="date" tickLine={false} axisLine={false} minTickGap={28} tickFormatter={value => value.slice(5).replace('-', '/')} />
          <YAxis tickLine={false} axisLine={false} width={48} tickFormatter={value => new Intl.NumberFormat(i18n.language, { notation: 'compact' }).format(value)} />
          <ChartTooltip content={<ChartTooltipContent />} />
          <ReferenceLine y={0} stroke="var(--border)" />
          <Bar isAnimationActive={false} dataKey="delta" radius={3} maxBarSize={28}>{data.days.map(day => <Cell key={day.date} fill={(day.delta ?? 0) < 0 ? 'var(--nova-danger)' : 'var(--nova-success)'} />)}</Bar>
        </BarChart>
      </ChartContainer> : <ChartEmpty label={t('statistics.noWritingHistory')} />}
    </ChartPanel>
    <ChartPanel title={t('statistics.manuscriptDistribution')}>
      {data.projects.length ? <StatisticsTable label={t('statistics.manuscriptDistribution')} rows={data.projects} rowKey={item => item.id} defaultSort="words" columns={[
        { id: 'name', label: t('statistics.book'), render: item => item.name },
        { id: 'words', label: t('statistics.manuscript'), render: item => number(item.words), sortValue: item => item.words },
        { id: 'chapters', label: t('statistics.chapters'), render: item => number(item.chapters), sortValue: item => item.chapters },
        { id: 'confirmed', label: t('statistics.chapterProgress'), sortValue: item => item.chapters ? item.confirmed / item.chapters : null, render: item => <div className="flex min-w-28 flex-col gap-2">
          <span>{number(item.confirmed)} / {number(item.chapters)}</span>
          <Progress value={item.chapters ? 100 * item.confirmed / item.chapters : 0} aria-label={t('statistics.chapterProgress')} />
        </div> },
      ]} /> : <ChartEmpty label={t('statistics.noBooks')} />}
    </ChartPanel>
    <p className="text-xs text-muted-foreground">{t('statistics.writingCoverage')}</p>
  </div>
}
