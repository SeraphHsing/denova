import { useState, type ReactNode } from 'react'
import { ArrowDown, ArrowUp } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface Column<T> {
  id: string
  label: string
  render: (row: T) => ReactNode
  sortValue?: (row: T) => number | string | null
}

// Missing observations always sort last, in either direction.
export function StatisticsTable<T>({ label, rows, columns, rowKey, defaultSort }: {
  label: string
  rows: T[]
  columns: Column<T>[]
  rowKey: (row: T) => string
  defaultSort: string
}) {
  const [sort, setSort] = useState({ id: defaultSort, descending: true })
  const sortValue = columns.find(column => column.id === sort.id)?.sortValue
  const ordered = sortValue ? [...rows].sort((a, b) => {
    const left = sortValue(a), right = sortValue(b)
    if (left === null) return right === null ? 0 : 1
    if (right === null) return -1
    const comparison = typeof left === 'number' && typeof right === 'number' ? left - right : String(left).localeCompare(String(right))
    return sort.descending ? -comparison : comparison
  }) : rows
  return <div className="overflow-x-auto" role="region" aria-label={label} tabIndex={0}>
    <table className="w-full text-sm">
      <caption className="sr-only">{label}</caption>
      <thead><tr className="border-b">{columns.map((column, index) => <th key={column.id} scope="col" className={cn('px-2 py-1 text-right font-normal whitespace-nowrap text-muted-foreground', index === 0 && 'text-left')}
        aria-sort={column.id === sort.id ? sort.descending ? 'descending' : 'ascending' : undefined}>
        {column.sortValue ? <Button variant="ghost" size="sm" onClick={() => setSort({ id: column.id, descending: sort.id === column.id ? !sort.descending : true })}>
          {column.label}{column.id === sort.id && (sort.descending ? <ArrowDown data-icon="inline-end" /> : <ArrowUp data-icon="inline-end" />)}
        </Button> : column.label}
      </th>)}</tr></thead>
      <tbody>{ordered.map(row => <tr key={rowKey(row)} className="border-b last:border-0">
        {columns.map((column, index) => <td key={column.id} className={cn('px-2 py-3 text-right align-middle whitespace-nowrap tabular-nums', index === 0 && 'min-w-40 max-w-64 text-left whitespace-normal wrap-anywhere')}>{column.render(row)}</td>)}
      </tr>)}</tbody>
    </table>
  </div>
}
