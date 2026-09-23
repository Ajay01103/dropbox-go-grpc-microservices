"use client"

import { useEffect } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import { X } from "lucide-react"
import { useForm } from "react-hook-form"
import * as z from "zod"

import { Button } from "@/components/ui/button"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { useCreateFolder } from "../api/use-files"
import type { Folder } from "@/gen/pb/metadata/metadata_pb"

const createFolderSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, "Folder name is required")
    .max(100, "Folder name must be 100 characters or less"),
})

type CreateFolderValues = z.infer<typeof createFolderSchema>

interface CreateFolderDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (folder: Folder) => void
}

export function CreateFolderDialog({ open, onOpenChange, onCreated }: CreateFolderDialogProps) {
  const createFolder = useCreateFolder()
  const form = useForm<CreateFolderValues>({
    resolver: zodResolver(createFolderSchema),
    defaultValues: { name: "" },
  })

  useEffect(() => {
    if (open) {
      form.reset({ name: "" })
    }
  }, [form, open])

  const onSubmit = async ({ name }: CreateFolderValues) => {
    try {
      const folder = await createFolder.mutateAsync({ parentId: "", name })

      onCreated(folder)
      onOpenChange(false)
    } catch (error) {
      form.setError("root", {
        message: error instanceof Error ? error.message : "Unable to create folder",
      })
    }
  }

  if (!open) return null

  const rootError = form.formState.errors.root?.message

  return (
    <div
      aria-labelledby="create-folder-title"
      aria-modal="true"
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/55 p-4"
      onMouseDown={() => onOpenChange(false)}
      role="dialog"
    >
      <div
        className="bg-background w-full max-w-130 rounded-2xl p-7 shadow-2xl sm:p-8"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="mb-5 flex items-start justify-between gap-4">
          <div>
            <h2
              className="text-base font-semibold"
              id="create-folder-title"
            >
              Create folder
            </h2>
            <p className="text-muted-foreground mt-1 text-sm">
              Give your new folder a name to get started.
            </p>
          </div>
          <Button
            aria-label="Close create folder dialog"
            className="-mt-2 -mr-2 rounded-lg"
            onClick={() => onOpenChange(false)}
            size="icon"
            type="button"
            variant="ghost"
          >
            <X />
          </Button>
        </div>

        <form onSubmit={form.handleSubmit(onSubmit)}>
          <FieldGroup>
            <Field data-invalid={Boolean(form.formState.errors.name)}>
              <FieldLabel htmlFor="folder-name">Folder name</FieldLabel>
              <Input
                autoFocus
                id="folder-name"
                placeholder="New Folder"
                {...form.register("name")}
                aria-invalid={Boolean(form.formState.errors.name)}
              />
              {form.formState.errors.name && (
                <FieldDescription className="text-destructive">
                  {form.formState.errors.name.message}
                </FieldDescription>
              )}
            </Field>

            {rootError && (
              <FieldDescription className="text-destructive">{rootError}</FieldDescription>
            )}

            <div className="flex items-center justify-end gap-2 pt-3">
              <Button
                onClick={() => onOpenChange(false)}
                type="button"
                variant="ghost"
              >
                Cancel
              </Button>
              <Button
                disabled={form.formState.isSubmitting}
                type="submit"
              >
                {form.formState.isSubmitting ? "Creating..." : "Create"}
              </Button>
            </div>
          </FieldGroup>
        </form>
      </div>
    </div>
  )
}
