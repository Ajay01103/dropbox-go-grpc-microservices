// Folder routes use a parallel @modal slot: {children} is the folder grid
// (page.tsx), {modal} carries the intercepted preview overlay. On soft
// navigation the grid stays mounted underneath the drawer; a direct load
// renders @modal/default.tsx (no overlay) with the full-page preview instead.
export default function FolderLayout({
  children,
  modal,
}: {
  children: React.ReactNode
  modal: React.ReactNode
}) {
  return (
    <>
      {children}
      {modal}
    </>
  )
}
