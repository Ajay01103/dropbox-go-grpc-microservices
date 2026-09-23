import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { DashboardSidebar } from "@/modules/dashboard/components/dashboard-sidebar"
import UploadDrawer from "@/modules/files/components/upload-drawer"

const DashboardLayout = async ({ children }: { children: React.ReactNode }) => {
  return (
    <SidebarProvider
      defaultOpen={true}
      className="h-svh"
      style={{ "--sidebar-width": "350px" } as React.CSSProperties}
    >
      <DashboardSidebar />
      <SidebarInset className="min-h-0 min-w-0">
        <main className="flex min-h-0 flex-1 flex-col">{children}</main>
      </SidebarInset>
      <UploadDrawer />
    </SidebarProvider>
  )
}

export default DashboardLayout
