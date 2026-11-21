export interface HashFileCommand {
  type: 'hash-file'
  jobId: string
  file: File
  chunkSizeBytes: number
}

export interface CancelHashCommand {
  type: 'cancel'
  jobId: string
}

export type FileHashWorkerCommand = HashFileCommand | CancelHashCommand

export interface FileHashProgressEvent {
  type: 'progress'
  jobId: string
  processedBytes: number
  totalBytes: number
}

export interface FileHashCompletedEvent {
  type: 'completed'
  jobId: string
  sha256: string
}

export interface FileHashCanceledEvent {
  type: 'canceled'
  jobId: string
}

export interface FileHashFailedEvent {
  type: 'failed'
  jobId: string
  message: string
}

export type FileHashWorkerEvent =
  FileHashProgressEvent | FileHashCompletedEvent | FileHashCanceledEvent | FileHashFailedEvent
