import type { BookSummary } from '../../api/types'
import { VolumeTile } from './VolumeTile'

/**
 * The volume grid (ui-spec §5.3): `repeat(auto-fill, minmax(128px, 1fr))`.
 *
 * `books` is rendered **in array order** — the contract says `SeriesDetail.books`
 * is already natural-sorted by `ord` (FR-IDX-007) and WP-10 acceptance 3 says
 * the UI never re-sorts. Sorting here would be a second, disagreeing
 * implementation of natural order, and `01권/` vs `01권.zip` (E-5) is exactly
 * the case where the two would differ.
 *
 * The volume *number* is `ord + 1` rather than anything parsed out of the file
 * name: E-5 means a series legitimately contains `07권.zip`,
 * `07권.repair.zip` and `07권 (2).repair.zip`, so a name-derived number would
 * print `7` three times.
 */
export interface VolumeGridProps {
  books: readonly BookSummary[]
  onOpen: (book: BookSummary) => void
}

export function VolumeGrid({ books, onOpen }: VolumeGridProps) {
  return (
    <div
      data-testid="volume-grid"
      className="grid gap-3 p-4"
      style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(128px, 1fr))' }}
    >
      {books.map((book) => (
        <VolumeTile key={book.id} book={book} number={book.ord + 1} onOpen={onOpen} />
      ))}
    </div>
  )
}
