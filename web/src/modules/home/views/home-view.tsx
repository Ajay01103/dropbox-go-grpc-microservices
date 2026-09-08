import { HomeMainHeader } from "../components/home-main-header";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Clock3, Star } from "lucide-react";

const HomeView = () => {
  return (
    <div className="flex min-h-screen flex-col bg-background">
      <HomeMainHeader />
      <main className="flex flex-1 flex-col px-4 py-4 sm:px-8 sm:py-6">
        <Tabs defaultValue="recents" className="flex min-h-0 flex-1">
          <TabsList className="h-10 rounded-full bg-muted p-1">
            <TabsTrigger value="recents" className="h-8 rounded-full px-4 text-sm">
              <Clock3 />
              Recents
            </TabsTrigger>
            <TabsTrigger value="starred" className="h-8 rounded-full px-4 text-sm">
              <Star />
              Starred
            </TabsTrigger>
          </TabsList>
          <TabsContent value="recents" className="mt-4 flex min-h-0 flex-1 flex-col">
            <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-muted-foreground">
              No recent files
            </div>
          </TabsContent>
          <TabsContent value="starred" className="mt-4 flex min-h-0 flex-1 flex-col">
            <div className="flex flex-1 items-center justify-center rounded-2xl border border-dashed text-sm text-muted-foreground">
              No starred files
            </div>
          </TabsContent>
        </Tabs>
      </main>
    </div>
  );
};

export default HomeView;
