import { useState } from 'react'
import { useParams, Link } from 'react-router-dom'
import { useFeedMembers } from '../hooks/useFeedMembers'
import { Card, CardContent, CardHeader, CardTitle } from './ui/card'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { AddUserDialog } from './AddUserDialog'
import { RemoveUserDialog } from './RemoveUserDialog'
import { ArrowLeft, Users, Loader2, ChevronsLeft, ChevronsRight, Search, X } from 'lucide-react'

function Pagination({
  currentPage,
  totalPages,
  onPageChange,
}: {
  currentPage: number
  totalPages: number
  onPageChange: (page: number) => void
}) {
  // Generate page numbers to show: current page +/- 2, plus first and last
  const getPageNumbers = () => {
    const pages: (number | 'ellipsis')[] = []

    // Always show first page
    pages.push(1)

    // Calculate range around current page
    const rangeStart = Math.max(2, currentPage - 2)
    const rangeEnd = Math.min(totalPages - 1, currentPage + 2)

    // Add ellipsis after first page if needed
    if (rangeStart > 2) {
      pages.push('ellipsis')
    }

    // Add pages in range
    for (let i = rangeStart; i <= rangeEnd; i++) {
      pages.push(i)
    }

    // Add ellipsis before last page if needed
    if (rangeEnd < totalPages - 1) {
      pages.push('ellipsis')
    }

    // Always show last page (if more than 1 page)
    if (totalPages > 1) {
      pages.push(totalPages)
    }

    return pages
  }

  if (totalPages <= 1) return null

  const pageNumbers = getPageNumbers()

  return (
    <div className="flex items-center justify-center gap-1 flex-wrap">
      <Button
        variant="outline"
        size="sm"
        onClick={() => onPageChange(1)}
        disabled={currentPage === 1}
        title="First page"
      >
        <ChevronsLeft className="h-4 w-4" />
      </Button>

      {pageNumbers.map((page, idx) =>
        page === 'ellipsis' ? (
          <span key={`ellipsis-${idx}`} className="px-2 text-muted-foreground">
            ...
          </span>
        ) : (
          <Button
            key={page}
            variant={page === currentPage ? 'default' : 'outline'}
            size="sm"
            onClick={() => onPageChange(page)}
            className="min-w-10"
          >
            {page}
          </Button>
        )
      )}

      <Button
        variant="outline"
        size="sm"
        onClick={() => onPageChange(totalPages)}
        disabled={currentPage === totalPages}
        title="Last page"
      >
        <ChevronsRight className="h-4 w-4" />
      </Button>
    </div>
  )
}

export function FeedDetail() {
  const { feedName } = useParams<{ feedName: string }>()
  const decodedFeedName = feedName ? decodeURIComponent(feedName) : ''
  const [currentPage, setCurrentPage] = useState(1)
  const [searchInput, setSearchInput] = useState('')
  const [searchQuery, setSearchQuery] = useState('')

  const { data, isLoading, error } = useFeedMembers(decodedFeedName, currentPage, searchQuery)

  const handleSearch = () => {
    setSearchQuery(searchInput)
    setCurrentPage(1) // Reset to first page when searching
  }

  const handleClearSearch = () => {
    setSearchInput('')
    setSearchQuery('')
    setCurrentPage(1)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter') {
      handleSearch()
    }
  }

  const members = data?.members ?? []
  const totalCount = data?.totalCount ?? 0
  const totalPages = data?.totalPages ?? 1

  if (!feedName) {
    return (
      <div className="text-center py-12 text-destructive">
        Feed name is required
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center gap-4">
        <Link to="/">
          <Button variant="ghost" size="sm">
            <ArrowLeft className="h-4 w-4 mr-2" />
            Back
          </Button>
        </Link>
        <div className="flex-1">
          <h2 className="text-xl font-semibold">{decodedFeedName}</h2>
          <p className="text-sm text-muted-foreground flex items-center gap-1">
            <Users className="h-3 w-3" />
            {totalCount} members
          </p>
        </div>
        <AddUserDialog feedName={decodedFeedName} />
      </div>

      {isLoading ? (
        <div className="flex items-center justify-center py-12">
          <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
        </div>
      ) : error ? (
        <Card>
          <CardContent className="pt-6">
            <div className="text-center text-destructive">
              Failed to load members: {error.message}
            </div>
          </CardContent>
        </Card>
      ) : members.length === 0 ? (
        <Card>
          <CardContent className="pt-6">
            <div className="text-center text-muted-foreground">
              No members in this feed. Add users to get started.
            </div>
          </CardContent>
        </Card>
      ) : (
        <>
          <Card>
            {/* v4 space-y margins the title rather than the search row; keep v3's offset on the search row */}
            <CardHeader className="flex flex-row items-center justify-between gap-4 flex-wrap space-y-0">
              <CardTitle className="text-base">Feed Members</CardTitle>
              <div className="flex items-center gap-2 flex-1 justify-end mt-1.5">
                <div className="relative max-w-xs">
                  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
                  <Input
                    placeholder="Search handle or DID..."
                    value={searchInput}
                    onChange={(e) => setSearchInput(e.target.value)}
                    onKeyDown={handleKeyDown}
                    className="pl-9 pr-9 h-8 text-sm w-48"
                  />
                  {(searchInput || searchQuery) && (
                    <button
                      onClick={handleClearSearch}
                      className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  )}
                </div>
                <span className="text-sm text-muted-foreground whitespace-nowrap">
                  Page {currentPage} of {totalPages}
                </span>
              </div>
            </CardHeader>
            <CardContent className="p-0">
              <table className="w-full">
                <thead>
                  <tr className="border-b bg-muted/50">
                    <th className="text-left text-xs font-medium text-muted-foreground px-4 py-3">User</th>
                    <th className="text-left text-xs font-medium text-muted-foreground px-4 py-3 hidden md:table-cell">Handle</th>
                    <th className="text-left text-xs font-medium text-muted-foreground px-4 py-3 hidden sm:table-cell">Added</th>
                    <th className="text-right text-xs font-medium text-muted-foreground px-4 py-3 w-16"></th>
                  </tr>
                </thead>
                <tbody>
                  {members.map((member) => (
                    <tr key={member.did} className="border-b last:border-0 hover:bg-muted/30">
                      <td className="px-4 py-3">
                        <a
                          href={`https://bsky.app/profile/${member.did}`}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="hover:underline"
                        >
                          {member.displayName || member.handle ? (
                            <span className="text-sm font-medium text-primary">
                              {member.displayName || member.handle}
                            </span>
                          ) : (
                            <span className="text-sm font-medium font-mono text-primary">
                              {member.did.slice(0, 32)}...
                            </span>
                          )}
                        </a>
                        {/* Show handle on mobile if no display name */}
                        {member.handle && !member.displayName && (
                          <span className="md:hidden text-xs text-muted-foreground ml-2">
                            @{member.handle}
                          </span>
                        )}
                      </td>
                      <td className="px-4 py-3 hidden md:table-cell">
                        {member.handle ? (
                          <span className="text-sm text-muted-foreground">@{member.handle}</span>
                        ) : (
                          <span className="text-sm text-muted-foreground/50">—</span>
                        )}
                      </td>
                      <td className="px-4 py-3 hidden sm:table-cell">
                        <span className="text-sm text-muted-foreground">
                          {new Date(member.createdAt).toLocaleDateString()}
                        </span>
                      </td>
                      <td className="px-4 py-3 text-right">
                        <RemoveUserDialog
                          feedName={decodedFeedName}
                          userDid={member.did}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </CardContent>
          </Card>

          <Pagination
            currentPage={currentPage}
            totalPages={totalPages}
            onPageChange={setCurrentPage}
          />
        </>
      )}
    </div>
  )
}
