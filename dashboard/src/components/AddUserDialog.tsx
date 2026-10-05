import { useState } from 'react'
import { useAddUser } from '../hooks/useFeedMembers'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Label } from './ui/label'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from './ui/dialog'
import { Plus, Loader2 } from 'lucide-react'

interface AddUserDialogProps {
  feedName: string
}

export function AddUserDialog({ feedName }: AddUserDialogProps) {
  const [open, setOpen] = useState(false)
  const [handle, setHandle] = useState('')
  const [error, setError] = useState('')

  const addUser = useAddUser()

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')

    const trimmedHandle = handle.trim()
    if (!trimmedHandle) {
      setError('Handle is required')
      return
    }

    try {
      await addUser.mutateAsync({ feedName, handle: trimmedHandle })
      setHandle('')
      setOpen(false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to add user')
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button>
          <Plus className="h-4 w-4 mr-2" />
          Add User
        </Button>
      </DialogTrigger>
      <DialogContent>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Add User to Feed</DialogTitle>
            <DialogDescription>
              Enter the Bluesky handle of the user you want to add to{' '}
              <span className="font-medium">{feedName}</span>.
            </DialogDescription>
          </DialogHeader>
          <div className="py-4 space-y-4">
            <div className="space-y-2">
              <Label htmlFor="handle">Bluesky Handle</Label>
              {/* space-y-2 margins the inline Label in v4, where it has no effect, so the gap lives on the Input */}
              <Input
                className="mt-2"
                id="handle"
                placeholder="user.bsky.social"
                value={handle}
                onChange={(e) => {
                  setHandle(e.target.value)
                  setError('')
                }}
                disabled={addUser.isPending}
              />
              {error && <p className="text-sm text-destructive">{error}</p>}
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
              disabled={addUser.isPending}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={addUser.isPending}>
              {addUser.isPending ? (
                <>
                  <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                  Adding...
                </>
              ) : (
                'Add User'
              )}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
