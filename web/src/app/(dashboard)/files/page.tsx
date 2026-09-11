"use client";

import { useAuth } from "@/lib/auth-context";
import { FileView } from "@/modules/files/views/file-view";

const FilesPage = () => {
  const { currentUser, isLoadingAuth } = useAuth();

  if (isLoadingAuth || !currentUser) return null;

  return <FileView />;
};

export default FilesPage;
