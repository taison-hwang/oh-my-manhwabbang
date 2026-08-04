import type { BookSummary } from '../../api/types'
import { VolumeRow } from './VolumeRow'

/**
 * The volume list (ui-spec §5.4). No header row: volumes are naturally ordered
 * and sorting is not offered here — that is the library screen's job.
 *
 * Array order is preserved for the same reason as `VolumeGrid`: `books[]` is
 * already sorted by `ord` and the UI never re-sorts (WP-10 acceptance 3).
 *
 * E-32 makes it a card — `--radius-lg`, `--color-surface`, `--shadow-md`, 8px
 * of padding with 14 at the bottom — and the rows inside it lose their 1px
 * dividers to `.row-chip`. This list is not virtualised, so unlike the library
 * list there is no skeleton to keep in step and the card can live here.
 */
export interface VolumeListProps {
  books: readonly BookSummary[]
  onOpen: (book: BookSummary) => void
}

export function VolumeList({ books, onOpen }: VolumeListProps) {
  return (
    <div
      data-testid="volume-list"
      className="mx-4 mb-4 flex flex-col rounded-lg bg-surface p-2 pb-[14px] shadow-md"
    >
      {books.map((book) => (
        <VolumeRow key={book.id} book={book} onOpen={onOpen} />
      ))}
    </div>
  )
}
