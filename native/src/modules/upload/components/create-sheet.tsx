import { MaterialCommunityIcons } from '@expo/vector-icons';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ComponentProps,
  type ReactNode,
} from 'react';
import { Pressable, TextInput, useColorScheme } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { Sheet, Spinner, Text, XStack, YStack } from 'tamagui';

import { fileQueryKeys } from '@/modules/files/api/use-files';
import { metadataFolderRpcClient } from '@/lib/rpc';

/**
 * The sheet behind the tab bar's "+" button.
 *
 * One provider owns the open state so every entry point (the tab button, the
 * home header's upload icon) opens the same sheet instead of each screen
 * mounting its own. Only "Create folder" reaches the backend today; the rest
 * are visible but answer with an inline note rather than pretending to upload.
 */

/** Which pane the sheet opens on: the full menu, or straight to New folder. */
export type CreateSheetMode = 'menu' | 'folder';

type CreateSheetContextValue = {
  openCreateSheet: (options?: { mode?: CreateSheetMode }) => void;
};

const CreateSheetContext = createContext<CreateSheetContextValue | null>(null);

export function useCreateSheet(): CreateSheetContextValue {
  const context = useContext(CreateSheetContext);
  if (!context) {
    throw new Error('useCreateSheet must be used inside CreateSheetProvider');
  }
  return context;
}

type IconName = ComponentProps<typeof MaterialCommunityIcons>['name'];

const PRESSABLE_OPACITY = 0.55;

type Tile = { key: string; label: string; icon: IconName; note: string };

const TILES: Tile[] = [
  {
    key: 'files',
    label: 'Upload files',
    icon: 'file-upload-outline',
    note: 'Uploading from this device is coming soon.',
  },
  {
    key: 'folder',
    label: 'Upload folder',
    icon: 'folder-upload-outline',
    note: 'Uploading a folder from this device is coming soon.',
  },
  {
    key: 'photos',
    label: 'Upload photos',
    icon: 'image-plus',
    note: 'Uploading photos from this device is coming soon.',
  },
];

type Action = { key: string; label: string; icon: IconName; note?: string; onCreateFolder?: true };

const ACTIONS: Action[] = [
  { key: 'create-folder', label: 'Create folder', icon: 'folder-plus-outline', onCreateFolder: true },
  {
    key: 'create-file',
    label: 'Create file',
    icon: 'file-plus-outline',
    note: 'Creating a blank document is coming soon.',
  },
  { key: 'scan', label: 'Scan files', icon: 'scan-helper', note: 'Scanning is coming soon.' },
  {
    key: 'camera',
    label: 'Take a photo or video',
    icon: 'camera-outline',
    note: 'The camera is coming soon.',
  },
  {
    key: 'transfer',
    label: 'Transfer a copy',
    icon: 'chevron-double-right',
    note: 'Transfers are coming soon.',
  },
  {
    key: 'desktop',
    label: 'Link your desktop',
    icon: 'monitor',
    note: 'Linking a desktop is coming soon.',
  },
];

export function CreateSheetProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ open: boolean; mode: CreateSheetMode }>({
    open: false,
    mode: 'menu',
  });

  const contextValue = useMemo<CreateSheetContextValue>(
    () => ({
      openCreateSheet: (options) =>
        setState({ open: true, mode: options?.mode ?? 'menu' }),
    }),
    [],
  );

  return (
    <CreateSheetContext.Provider value={contextValue}>
      {children}
      <CreateSheet
        open={state.open}
        mode={state.mode}
        onOpenChange={(next) =>
          setState((previous) => ({ ...previous, open: next }))
        }
      />
    </CreateSheetContext.Provider>
  );
}

function CreateSheet({
  open,
  mode,
  onOpenChange,
}: {
  open: boolean;
  mode: CreateSheetMode;
  onOpenChange: (open: boolean) => void;
}) {
  const insets = useSafeAreaInsets();
  const dark = useColorScheme() === 'dark';
  const queryClient = useQueryClient();

  const [creatingFolder, setCreatingFolder] = useState(false);
  const [folderName, setFolderName] = useState('');
  const [notice, setNotice] = useState<string | null>(null);
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const colors = {
    surface: dark ? '#15171C' : '#F5F3EF',
    card: dark ? '#23262E' : '#FFFFFF',
    ink: dark ? '#F3F4F6' : '#111214',
    muted: dark ? '#9AA0A6' : '#5F6368',
    divider: dark ? '#2C303A' : '#EDE9E2',
    handle: dark ? '#3A3F49' : '#CBC6BD',
    primary: '#2563EB',
    primaryInk: '#FFFFFF',
  } as const;

  const showNotice = useCallback((message: string) => {
    setNotice(message);
    if (noticeTimer.current) clearTimeout(noticeTimer.current);
    noticeTimer.current = setTimeout(() => setNotice(null), 2400);
  }, []);

  useEffect(
    () => () => {
      if (noticeTimer.current) clearTimeout(noticeTimer.current);
    },
    [],
  );

  const createFolder = useMutation({
    mutationFn: async (name: string) => {
      const root = await metadataFolderRpcClient.getOrCreateRootFolder({});
      const rootId = root.folder?.folderId;
      if (!rootId) throw new Error('Could not resolve your root folder.');
      return metadataFolderRpcClient.createFolder({ parentId: rootId, name: name.trim() });
    },
    onSuccess: async () => {
      // The folder appears in the recent feed and in every folder listing, so
      // the whole file tree is dropped rather than one page.
      await queryClient.invalidateQueries({ queryKey: fileQueryKeys.all });
      closeSheet();
    },
  });

  // Closing resets the visit synchronously: a reopened sheet must not remember
  // a half-typed folder name or a note from the last time it was open.
  const closeSheet = useCallback(() => {
    onOpenChange(false);
    setCreatingFolder(false);
    setFolderName('');
    setNotice(null);
    createFolder.reset();
  }, [createFolder, onOpenChange]);

  const submitFolder = () => {
    const name = folderName.trim();
    if (!name || createFolder.isPending) return;
    createFolder.mutate(name);
  };

  return (
    <Sheet
      modal
      open={open}
      onOpenChange={(next: boolean) => (next ? onOpenChange(true) : closeSheet())}
      snapPoints={[78]}
      snapPointsMode="percent"
      dismissOnSnapToBottom
      moveOnKeyboardChange>
      <Sheet.Overlay
        bg="black"
        opacity={0.45}
        transition="quick"
      />
      <Sheet.Container
        bg={colors.surface}
        rounded="6"
      >
        <Sheet.Background
          bg={colors.surface}
          rounded="6"
        />
        <Sheet.Handle
          bg={colors.handle}
          w={44}
          h={5}
          mx="auto"
          mt={12}
          mb={8}
          rounded={999}
        />

        <Sheet.ScrollView
          px={0}
          contentContainerStyle={{
            paddingHorizontal: 16,
            paddingTop: 6,
            paddingBottom: insets.bottom + 20,
          }}
          showsVerticalScrollIndicator={false}
        >
          <YStack
            w="100%"
            maxW={560}
            mx="auto"
            gap={14}
          >
            {creatingFolder || mode === 'folder' ? (
              <YStack gap={14}>
                <Text
                  color={colors.ink}
                  fontSize={20}
                  fontWeight="700">
                  New folder
                </Text>

                <XStack
                  items="center"
                  gap={10}
                  h={54}
                  px={14}
                  rounded={14}
                  bg={colors.card}>
                  <MaterialCommunityIcons
                    name="folder-outline"
                    size={22}
                    color={colors.muted}
                  />
                  <TextInput
                    autoFocus
                    value={folderName}
                    onChangeText={setFolderName}
                    onSubmitEditing={submitFolder}
                    placeholder="Folder name"
                    placeholderTextColor={colors.muted}
                    returnKeyType="done"
                    maxLength={255}
                    style={{ flex: 1, minWidth: 0, fontSize: 16, color: colors.ink }}
                    accessibilityLabel="Folder name"
                  />
                </XStack>

                {createFolder.isError ? (
                  <Text
                    color="#DC2626"
                    fontSize={13}>
                    {createFolder.error instanceof Error
                      ? createFolder.error.message
                      : 'Could not create the folder.'}
                  </Text>
                ) : null}

                <XStack gap={10}>
                  <Pressable
                    accessibilityRole="button"
                    accessibilityLabel="Cancel"
                    style={{ flex: 1 }}
                    onPress={() => {
                      setFolderName('');
                      createFolder.reset();
                      // Opened straight into New folder: Cancel leaves the
                      // sheet entirely instead of dropping to a menu the user
                      // never asked for.
                      if (mode === 'folder') closeSheet();
                      else setCreatingFolder(false);
                    }}>
                    {({ pressed }) => (
                      <XStack
                        h={50}
                        items="center"
                        justify="center"
                        rounded={14}
                        bg={colors.card}
                        opacity={pressed ? PRESSABLE_OPACITY : 1}>
                        <Text
                          color={colors.ink}
                          fontSize={16}
                          fontWeight="600">
                          Cancel
                        </Text>
                      </XStack>
                    )}
                  </Pressable>

                  <Pressable
                    accessibilityRole="button"
                    accessibilityLabel="Create folder"
                    disabled={!folderName.trim() || createFolder.isPending}
                    style={{ flex: 1 }}
                    onPress={submitFolder}>
                    {({ pressed }) => (
                      <XStack
                        h={50}
                        items="center"
                        justify="center"
                        gap={8}
                        rounded={14}
                        bg={colors.primary}
                        opacity={!folderName.trim() ? 0.5 : pressed ? PRESSABLE_OPACITY : 1}>
                        {createFolder.isPending ? (
                          <Spinner
                            color={colors.primaryInk}
                            size="small"
                          />
                        ) : null}
                        <Text
                          color={colors.primaryInk}
                          fontSize={16}
                          fontWeight="700">
                          {createFolder.isPending ? 'Creating…' : 'Create'}
                        </Text>
                      </XStack>
                    )}
                  </Pressable>
                </XStack>
              </YStack>
            ) : (
              <>
                <XStack gap={10}>
                  {TILES.map((tile) => (
                    <Pressable
                      key={tile.key}
                      accessibilityRole="button"
                      accessibilityLabel={tile.label}
                      style={{ flex: 1 }}
                      onPress={() => showNotice(tile.note)}>
                      {({ pressed }) => (
                        <YStack
                          h={118}
                          px={8}
                          py={14}
                          gap={10}
                          items="center"
                          justify="center"
                          rounded={14}
                          bg={colors.card}
                          opacity={pressed ? PRESSABLE_OPACITY : 1}>
                          <MaterialCommunityIcons
                            name={tile.icon}
                            size={30}
                            color={colors.ink}
                          />
                          <Text
                            color={colors.ink}
                            fontSize={14}
                            fontWeight="500"
                            text="center">
                            {tile.label}
                          </Text>
                        </YStack>
                      )}
                    </Pressable>
                  ))}
                </XStack>

                <YStack
                  overflow="hidden"
                  rounded={16}
                  bg={colors.card}
                  mt={4}>
                  {ACTIONS.map((action, index) => (
                    <YStack key={action.key}>
                      {index > 0 ? (
                        <YStack
                          h={1}
                          bg={colors.divider}
                        />
                      ) : null}
                      <Pressable
                        accessibilityRole="button"
                        accessibilityLabel={action.label}
                        onPress={() =>
                          action.onCreateFolder
                            ? setCreatingFolder(true)
                            : showNotice(action.note ?? 'Coming soon.')
                        }>
                        {({ pressed }) => (
                          <XStack
                            h={58}
                            items="center"
                            gap={14}
                            px={16}
                            opacity={pressed ? PRESSABLE_OPACITY : 1}>
                            <MaterialCommunityIcons
                              name={action.icon}
                              size={23}
                              color={colors.ink}
                            />
                            <Text
                              color={colors.ink}
                              fontSize={16}
                              numberOfLines={1}
                              flex={1}>
                              {action.label}
                            </Text>
                          </XStack>
                        )}
                      </Pressable>
                    </YStack>
                  ))}
                </YStack>
              </>
            )}
          </YStack>
        </Sheet.ScrollView>

        {notice ? (
          <YStack
            position="absolute"
            b={insets.bottom + 18}
            l={0}
            r={0}
            items="center"
            pointerEvents="none">
            <XStack
              px={16}
              py={10}
              rounded={999}
              bg={dark ? '#F3F4F6' : '#111214'}>
              <Text
                color={dark ? '#111214' : '#FFFFFF'}
                fontSize={13}
                fontWeight="600">
                {notice}
              </Text>
            </XStack>
          </YStack>
        ) : null}
      </Sheet.Container>
    </Sheet>
  );
}
