import { HomeHeader1 } from "./home-header-1"
import { HomeHeader2 } from "./home-header-2"
import type { Folder } from "@/gen/pb/metadata/metadata_pb"

export const HomeMainHeader = ({
  onFolderCreated,
}: {
  onFolderCreated: (folder: Folder) => void
}) => {
  return (
    <div className="bg-background">
      <HomeHeader1 />
      <HomeHeader2 onFolderCreated={onFolderCreated} />
    </div>
  )
}
