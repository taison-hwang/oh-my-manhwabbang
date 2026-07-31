import type { BookSummary } from '../../api/types'
import { VolumeRow } from './VolumeRow'

/**
 * The volume list (ui-spec §5.4). No header row: volumes are naturally ordered
 * and sorting is not offered here — that is the library screen's job.
 *
 * Array order is preserved for the same reason as `VolumeGrid`: `books[]` is
 * already sorted by `ord` and the UI never re-sorts (WP-10 acceptance 3).
 */
export interface VolumeListProps {
  books: readonly BookSummary[]
  onOpen: (book: BookSummary) => void
}

export function VolumeList({ books, onOpen }: VolumeListProps) {
  return (
    <div data-testid="volume-list" className="flex flex-col px-4 py-2">
      {books.map((book) => (
        <VolumeRow key={book.id} book={book} onOpen={onOpen} />
      ))}
    </div>
  )
}
