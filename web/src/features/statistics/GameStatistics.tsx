import { useTranslation } from 'react-i18next'
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { ChartEmpty, ChartPanel, MetricStrip } from './StatisticsCharts'
import { StatisticsTable } from './StatisticsTable'
import type { StatisticsReport } from './api'

export function GameStatistics({ data, projects }: { data: StatisticsReport['game']; projects: StatisticsReport['projects'] }) {
  const { t, i18n } = useTranslation()
  const number = (value: number) => new Intl.NumberFormat(i18n.language).format(value)
  const turns = data.days.reduce((sum, day) => sum + day.turns, 0)
  const activeDays = data.days.filter(day => day.turns > 0).length
  const activeStories = data.stories.filter(story => story.turns > 0)
  const periodStories = data.stories.filter(story => story.turns > 0 || story.new_branches > 0)
  const projectNames = new Map(projects.map(project => [project.id, project.name]))

  return <div className="flex flex-col gap-4">
    <MetricStrip items={[
      { label: t('statistics.gameTurns'), value: number(turns) },
      { label: t('statistics.activeDays'), value: number(activeDays) },
      { label: t('statistics.activeStories'), value: number(activeStories.length) },
      { label: t('statistics.newBranches'), value: number(data.new_branches) },
    ]} />
    <ChartPanel title={t('statistics.gameTrend')}>
      {turns ? <ChartContainer config={{ turns: { label: t('statistics.gameTurns'), color: 'var(--nova-success)' } }} className="h-48 w-full aspect-auto">
        <BarChart accessibilityLayer data={data.days} margin={{ top: 8, right: 8, left: 0 }}>
          <CartesianGrid vertical={false} />
          <XAxis dataKey="date" tickLine={false} axisLine={false} minTickGap={28} tickFormatter={value => value.slice(5).replace('-', '/')} />
          <YAxis allowDecimals={false} tickLine={false} axisLine={false} width={40} tickFormatter={number} />
          <ChartTooltip content={<ChartTooltipContent />} />
          <Bar isAnimationActive={false} dataKey="turns" fill="var(--color-turns)" radius={3} maxBarSize={28} />
        </BarChart>
      </ChartContainer> : <ChartEmpty label={t('statistics.noGameActivity')} />}
    </ChartPanel>
    {periodStories.length > 0 && <ChartPanel title={t('statistics.storyActivity')}>
      <StatisticsTable label={t('statistics.storyActivity')} rows={periodStories} rowKey={item => `${item.project_id}/${item.id}`} defaultSort="turns" columns={[
        { id: 'name', label: t('statistics.story'), render: item => <><div>{item.name}</div><div className="text-xs text-muted-foreground">{projectNames.get(item.project_id)}</div></> },
        { id: 'turns', label: t('statistics.gameTurns'), render: item => number(item.turns), sortValue: item => item.turns },
        { id: 'active', label: t('statistics.activeDays'), render: item => number(item.active_days), sortValue: item => item.active_days },
        { id: 'branches', label: t('statistics.newBranches'), render: item => number(item.new_branches), sortValue: item => item.new_branches },
        { id: 'last', label: t('statistics.lastActive'), render: item => item.last_active_date ? new Intl.DateTimeFormat(i18n.language, { month: 'short', day: 'numeric' }).format(new Date(`${item.last_active_date}T12:00:00`)) : '—', sortValue: item => item.last_active_date || null },
      ]} />
    </ChartPanel>}
    <p className="text-xs text-muted-foreground">{t('statistics.gameCoverage')}</p>
  </div>
}
