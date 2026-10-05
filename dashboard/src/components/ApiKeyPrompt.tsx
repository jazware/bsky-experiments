import { useState } from 'react'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Label } from './ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './ui/card'
import { setApiKey as saveApiKey } from '../lib/api'

interface ApiKeyPromptProps {
  onApiKeySet: () => void
}

export function ApiKeyPrompt({ onApiKeySet }: ApiKeyPromptProps) {
  const [inputValue, setInputValue] = useState('')
  const [error, setError] = useState('')

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    const trimmedKey = inputValue.trim()
    if (!trimmedKey) {
      setError('API key is required')
      return
    }
    saveApiKey(trimmedKey)
    onApiKeySet()
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader>
          <CardTitle>Feedgen Dashboard</CardTitle>
          <CardDescription>
            Enter your API key to access the dashboard
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="apiKey">API Key</Label>
              {/* space-y-2 margins the inline Label in v4, where it has no effect, so the gap lives on the Input */}
              <Input
                className="mt-2"
                id="apiKey"
                type="password"
                placeholder="Enter your API key"
                value={inputValue}
                onChange={(e) => {
                  setInputValue(e.target.value)
                  setError('')
                }}
              />
              {error && <p className="text-sm text-destructive">{error}</p>}
            </div>
            <Button type="submit" className="w-full">
              Continue
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
