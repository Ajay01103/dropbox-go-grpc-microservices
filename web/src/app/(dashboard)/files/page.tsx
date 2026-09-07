"use client"

import { FileUploader } from "@/modules/files/components/file-uploader"
import { useAuth } from "@/lib/auth-context"

const FilesPage = () => {
  const { currentUser, isLoadingAuth } = useAuth()

  if (isLoadingAuth || !currentUser) return null

  return (
    <div className="flex h-full flex-col gap-6 p-6">
      <h1 className="text-2xl font-bold">Files</h1>
      <FileUploader userId={currentUser.userId} />
    </div>
  )
}

export default FilesPage
