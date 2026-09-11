import { FileFilters } from "@/modules/files/components/file-filters"
import { FileHeader } from "@/modules/files/components/file-header"

export const FileView = () => {
  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <FileHeader />
      <FileFilters />

      <section className="min-h-0 flex-1 overflow-auto px-4 py-4 sm:px-6 lg:px-10">
        <div className="flex items-center gap-3">
          <span className="size-5 rounded border border-input bg-background" />
          <h2 className="text-base font-semibold">September 2026</h2>
          <span className="text-muted-foreground text-sm">1 item</span>
        </div>

        <div className="mt-4 grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-6 xl:grid-cols-8">
          <div className="bg-muted/40 aspect-square rounded-xl border" aria-label="File preview" />
        </div>
      </section>
    </div>
  )
}
