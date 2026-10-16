"use client"

import { useEffect, useRef } from "react"
import { zodResolver } from "@hookform/resolvers/zod"
import { useForm } from "react-hook-form"
import * as z from "zod"

import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet"
import { useIsMobile } from "@/hooks/use-mobile"
import { useRenameFile } from "@/modules/files/api/use-files"

// Mirrors the metadata service's name rules (validateName in the service layer):
// non-empty, no path separators, no leading/trailing whitespace, and neither "."
// nor ".." — so the dialog rejects the same names the server would, before the
// round trip instead of after it.
const renameFileSchema = z.object({
  name: z
    .string()
    .trim()
    .min(1, "File name is required")
    .max(100, "File name must be 100 characters or less")
    .refine((value) => !/[/\\]/.test(value), "A file name cannot contain “/” or “\\”")
    .refine((value) => value !== "." && value !== "..", "That name is reserved"),
})

type RenameFileValues = z.infer<typeof renameFileSchema>

interface RenameFileDialogProps {
  fileId: string
  currentName: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

// RenameFileDialog renames one file. It is a dialog on desktop and a bottom
// sheet on phones, and it owns its own
// mutation so every caller gets the same behaviour: validate locally, call
// RenameFile, keep the dialog open with an inline message if the server
// refuses, and let the mutation's invalidation refresh the grid.
export function RenameFileDialog({
  fileId,
  currentName,
  open,
  onOpenChange,
}: RenameFileDialogProps) {
  const renameFile = useRenameFile()
  const isMobile = useIsMobile()
  const inputRef = useRef<HTMLInputElement | null>(null)
  const form = useForm<RenameFileValues>({
    resolver: zodResolver(renameFileSchema),
    defaultValues: { name: currentName },
  })
  const { ref: nameFieldRef, ...nameField } = form.register("name")

  // Re-seed on every open: a rename that failed, or a card that re-rendered with
  // a new name, must not leave the previous attempt in the field.
  useEffect(() => {
    if (!open) return
    form.reset({ name: currentName })
    // Preselect the base name and leave the extension unselected, so typing
    // replaces the name without destroying the type — the behaviour people
    // already expect from a file manager's rename.
    const frame = requestAnimationFrame(() => {
      const input = inputRef.current
      if (!input) return
      input.focus()
      const dot = currentName.lastIndexOf(".")
      input.setSelectionRange(0, dot > 0 ? dot : currentName.length)
    })
    return () => cancelAnimationFrame(frame)
  }, [form, open, currentName])

  const onSubmit = async ({ name }: RenameFileValues) => {
    const nextName = name.trim()
    // Renaming to the same name is a no-op, not an error: close and move on.
    if (nextName === currentName) {
      onOpenChange(false)
      return
    }
    try {
      await renameFile.mutateAsync({ fileId, newName: nextName })
      onOpenChange(false)
    } catch (error) {
      form.setError("root", {
        message: error instanceof Error ? error.message : "Unable to rename file",
      })
    }
  }

  const rootError = form.formState.errors.root?.message

  const content = (
    <>
      {isMobile ? (
        <SheetHeader className="px-0 pt-0">
          <SheetTitle className="text-base font-semibold">Rename file</SheetTitle>
          <SheetDescription className="text-sm">
            The new name replaces “{currentName}” everywhere this file appears.
          </SheetDescription>
        </SheetHeader>
      ) : (
        <DialogHeader>
          <DialogTitle className="text-base font-semibold">Rename file</DialogTitle>
          <DialogDescription className="text-sm">
            The new name replaces “{currentName}” everywhere this file appears.
          </DialogDescription>
        </DialogHeader>
      )}

      <form onSubmit={form.handleSubmit(onSubmit)}>
        <FieldGroup>
          <Field data-invalid={Boolean(form.formState.errors.name)}>
            <FieldLabel htmlFor="rename-file-name">File name</FieldLabel>
            <Input
              {...nameField}
              aria-invalid={Boolean(form.formState.errors.name)}
              autoComplete="off"
              id="rename-file-name"
              ref={(element) => {
                nameFieldRef(element)
                inputRef.current = element
              }}
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

          <div className="flex flex-col-reverse gap-2 pt-2 sm:flex-row sm:justify-end">
            <Button
              onClick={() => onOpenChange(false)}
              type="button"
              variant="ghost"
            >
              Cancel
            </Button>
            <Button
              disabled={renameFile.isPending || !form.formState.isDirty}
              type="submit"
            >
              {renameFile.isPending ? "Renaming..." : "Rename"}
            </Button>
          </div>
        </FieldGroup>
      </form>
    </>
  )

  const handleOpenChange = (next: boolean) => {
    // The name is already committed once the request is in flight; closing
    // here would only hide the outcome.
    if (renameFile.isPending) return
    onOpenChange(next)
  }

  if (isMobile) {
    return (
      <Sheet open={open} onOpenChange={handleOpenChange}>
        <SheetContent
          className="gap-5 rounded-t-2xl px-4 pt-5 pb-[max(1.5rem,env(safe-area-inset-bottom))]"
          side="bottom"
          showCloseButton={false}
        >
          {content}
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
    >
      <DialogContent
        className="w-full max-w-lg gap-5 rounded-2xl p-6 shadow-2xl sm:p-7"
        showCloseButton={false}
      >
        {content}
      </DialogContent>
    </Dialog>
  )
}
