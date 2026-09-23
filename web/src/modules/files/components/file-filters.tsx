"use client"

const filterGroups = ["All Items", "Photos", "Videos", "Starred"]
const dateFilters = ["Years", "Months", "Days"]

function FilterButton({ active, children }: { active?: boolean; children: React.ReactNode }) {
  return (
    <button
      className={`shrink-0 rounded-lg px-3 py-2 text-sm font-semibold transition-colors ${
        active
          ? "bg-muted text-foreground shadow-[0_1px_2px_rgba(0,0,0,0.04)]"
          : "text-foreground/90 hover:bg-muted/70"
      }`}
      type="button"
    >
      {children}
    </button>
  )
}

export function FileFilters() {
  return (
    <div className="bg-background border-b px-4 sm:px-6 lg:px-10">
      <div className="flex min-h-16 flex-wrap items-center gap-x-5 gap-y-2 py-3">
        <div className="flex max-w-full [scrollbar-width:none] items-center gap-1 overflow-x-auto pb-0.5 [&::-webkit-scrollbar]:hidden">
          {filterGroups.map((filter, index) => (
            <FilterButton
              key={filter}
              active={index === 0}
            >
              {filter}
            </FilterButton>
          ))}
        </div>

        <div className="bg-border hidden h-8 w-px sm:block" />

        <div className="flex max-w-full [scrollbar-width:none] items-center gap-1 overflow-x-auto pb-0.5 [&::-webkit-scrollbar]:hidden">
          {dateFilters.map((filter) => (
            <FilterButton
              key={filter}
              active={filter === "Months"}
            >
              {filter}
            </FilterButton>
          ))}
        </div>

        <div className="bg-border hidden h-8 w-px sm:block" />
      </div>
    </div>
  )
}
