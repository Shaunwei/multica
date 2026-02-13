import { useEffect, useState, useCallback, useRef } from 'react'
import { Button } from '@multica/ui/components/ui/button'
import { Separator } from '@multica/ui/components/ui/separator'
import { ChevronLeft, Info, Check, Smartphone } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@multica/ui/components/ui/alert-dialog'
import { useHubStore, selectPrimaryAgent } from '../../../stores/hub'
import { TelegramConnectQR } from '../../../components/telegram-qr'
import { StepDots } from './step-dots'

interface DeviceMeta {
  userAgent?: string
  platform?: string
  language?: string
  clientName?: string
}

interface PendingConfirm {
  deviceId: string
  meta?: DeviceMeta
}

interface ConnectStepProps {
  onNext: () => void
  onBack: () => void
}

export default function ConnectStep({ onNext, onBack }: ConnectStepProps) {
  const { hubInfo, agents } = useHubStore()
  const primaryAgent = selectPrimaryAgent(agents)
  const [connected, setConnected] = useState(false)
  const [connectedDevice, setConnectedDevice] = useState<string | null>(null)
  const [pending, setPending] = useState<PendingConfirm | null>(null)

  // Listen for device confirm requests during onboarding
  useEffect(() => {
    window.electronAPI?.hub.onDeviceConfirmRequest((deviceId: string, meta?: DeviceMeta) => {
      setPending({ deviceId, meta })
    })
    return () => {
      window.electronAPI?.hub.offDeviceConfirmRequest()
    }
  }, [])

  const handleAllow = useCallback(() => {
    if (!pending) return
    window.electronAPI?.hub.deviceConfirmResponse(pending.deviceId, true)
    setConnectedDevice(pending.meta?.clientName ?? pending.deviceId)
    setPending(null)
    setConnected(true)
  }, [pending])

  const handleReject = useCallback(() => {
    if (!pending) return
    window.electronAPI?.hub.deviceConfirmResponse(pending.deviceId, false)
    setPending(null)
  }, [pending])

  const deviceLabel = pending?.meta?.clientName ?? pending?.deviceId

  return (
    <div className="h-full flex items-center justify-center px-6 py-8 animate-in fade-in duration-300">
      <div className="w-full max-w-md space-y-6">
        {/* Back button */}
        <button
          onClick={onBack}
          className="flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
        >
          <ChevronLeft className="size-4" />
          Back
        </button>

        {/* Header */}
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">
            Connect Telegram
          </h1>
          <p className="text-sm text-muted-foreground">
            Scan the QR code with your phone camera to connect on Telegram.
          </p>
        </div>

        {/* Info box */}
        <div className="rounded-lg bg-muted/50 px-4 py-3 space-y-2">
          <p className="text-sm text-muted-foreground">
            Chat with your agent from your phone via Telegram.
            Your messages are routed through the Gateway to this machine.
          </p>
          <p className="text-xs text-muted-foreground/70 flex items-center gap-1.5">
            <Info className="size-3.5 shrink-0" />
            Discord, Slack, and more coming soon.
          </p>
        </div>

        {/* QR code or connected state */}
        <div className="flex justify-center py-2">
          {connected ? (
            <div className="flex flex-col items-center gap-3 py-6">
              <div className="size-12 rounded-full bg-green-500/10 flex items-center justify-center">
                <Check className="size-6 text-green-500" />
              </div>
              <p className="text-sm font-medium">Telegram connected</p>
              {connectedDevice && (
                <div className="flex items-center gap-2 text-xs text-muted-foreground bg-muted/50 rounded-lg px-3 py-2">
                  <Smartphone className="size-3.5 shrink-0" />
                  <span>{connectedDevice}</span>
                </div>
              )}
            </div>
          ) : (
            <TelegramConnectQR
              gateway={hubInfo?.url ?? 'http://localhost:3000'}
              hubId={hubInfo?.hubId ?? 'unknown'}
              agentId={primaryAgent?.id ?? 'unknown'}
              expirySeconds={30}
              size={180}
            />
          )}
        </div>

        <Separator />

        {/* Footer */}
        <div className="flex items-center justify-between">
          <StepDots />
          <div className="flex gap-2">
            {!connected && (
              <Button size="sm" variant="outline" onClick={onNext}>
                Skip
              </Button>
            )}
            <Button size="sm" onClick={onNext}>
              Continue
            </Button>
          </div>
        </div>
      </div>

      {/* Device confirm dialog */}
      <AlertDialog open={pending !== null}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>New Device Connection</AlertDialogTitle>
            <AlertDialogDescription>
              <span className="font-medium">{deviceLabel}</span> wants to connect.
              <span className="block mt-1">Allow this device?</span>
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel onClick={handleReject}>
              Reject
            </AlertDialogCancel>
            <AlertDialogAction onClick={handleAllow}>
              Allow
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
