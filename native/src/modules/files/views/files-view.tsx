import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { useMemo, useState, type ComponentProps } from 'react';
import {
  Alert,
  FlatList,
  Linking,
  RefreshControl,
  Pressable,
  useColorScheme,
  useWindowDimensions,
  type ListRenderItem,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { Card, Image, Spinner, Text, XStack, YStack } from 'tamagui';

import type { Item } from '@/gen/pb/metadata/metadata_pb';
import { FileSort } from '@/gen/pb/metadata/metadata_pb';
import {
  useDeleteItem,
  useFolderItems,
  useRootFolder,
  useThumbnailUrls,
} from '@/modules/files/api/use-files';
import {
  FOLDER_TINT,
  describeFile,
  fileKind,
  fileKindIcon,
} from '@/modules/files/lib/file-display';
import { metadataRpcClient } from '@/lib/rpc';
import { useCreateSheet } from '@/modules/upload/components/create-sheet';
import { FileRow, ROW_PREVIEW } from '../components/file-row';

const LINK_BLUE = '#1A73E8';
const CONTENT_MAX_WIDTH = 880;
const TABLET_MIN_WIDTH = 768;
const GRID_GAP = 14;

type Crumb = { id: string; name: string };

/**
 * The Files tab: a folder browser over the metadata service.
 *
 * It is one route with a local crumb stack rather than a route per folder —
 * the tab bar's Files entry is a single destination, and keeping the stack in
 * state means drilling in is instant and the back chevron always knows where it
 * goes. Listing, sorting and thumbnails all come from `use-files`; nothing here
 * builds a transport or touches a token.
 */
export default function FilesView() {
  const insets = useSafeAreaInsets();
  const dark = useColorScheme() === 'dark';
  const { width } = useWindowDimensions();
  const { openCreateSheet } = useCreateSheet();

  const isWide = width >= TABLET_MIN_WIDTH;
  const gutter = isWide ? 28 : 16;
  const contentWidth = Math.min(width, CONTENT_MAX_WIDTH) - gutter * 2;
  const columns = isWide ? 4 : 2;
  const gridCardWidth = Math.floor((contentWidth - GRID_GAP * (columns - 1)) / columns);

  const root = useRootFolder();
  const [path, setPath] = useState<Crumb[]>([]);
  const [sort, setSort] = useState<FileSort>(FileSort.NAME);
  const [grid, setGrid] = useState(false);
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<Record<string, Item>>({});

  const current = path[path.length - 1];
  const folderId = current?.id ?? root.data?.folderId ?? '';
  const title = selecting
    ? `${Object.keys(selected).length} selected`
    : current?.name || 'Dropbox';

  const contents = useFolderItems(folderId, sort);
  const items = useMemo(() => {
    const seen = new Set<string>();
    const unique: Item[] = [];
    for (const page of contents.data?.pages ?? []) {
      for (const item of page.items) {
        const key = `${item.itemType}-${item.itemId}`;
        if (seen.has(key)) continue;
        seen.add(key);
        unique.push(item);
      }
    }
    return unique;
  }, [contents.data]);

  const fileIds = useMemo(
    () => items.filter((item) => item.details.case === 'file').map((item) => item.itemId),
    [items],
  );
  const { urls: thumbnailUrls } = useThumbnailUrls(fileIds);
  const deleteItem = useDeleteItem();

  const runDelete = () => {
    const targets = Object.values(selected);
    if (targets.length === 0) return;
    Alert.alert(
      targets.length === 1 ? `Delete “${itemName(targets[0])}”?` : `Delete ${targets.length} items?`,
      'They move to Deleted files and can be restored from the web app.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Delete',
          style: 'destructive',
          onPress: () => {
            void Promise.allSettled(
              targets.map((item) =>
                deleteItem.mutateAsync({
                  id: item.itemId,
                  type: item.itemType === 'folder' ? 'folder' : 'file',
                }),
              ),
            ).then(() => {
              setSelected({});
              setSelecting(false);
            });
          },
        },
      ],
    );
  };

  const toggleSelected = (item: Item) => {
    setSelected((previous) => {
      const next = { ...previous };
      if (next[item.itemId]) delete next[item.itemId];
      else next[item.itemId] = item;
      return next;
    });
  };

  const openItem = (item: Item) => {
    if (selecting) {
      toggleSelected(item);
      return;
    }

    if (item.details.case === 'folder') {
      const folder = item.details.value;
      setPath((previous) => [
        ...previous,
        { id: folder?.folderId || item.itemId, name: folder?.name || 'Folder' },
      ]);
      return;
    }

    const file = item.details.case === 'file' ? item.details.value : undefined;
    if (file) {
      Alert.alert(file.filename, describeFile(file, ' · '), [
        { text: 'Close', style: 'cancel' },
        { text: 'Download', onPress: () => showDownload(file.fileId, file.filename) },
      ]);
    }
  };

  // Downloads hand the presigned ticket URL to the system handler; the bytes
  // never pass through this app.
  const showDownload = (fileId: string, filename: string) => {
    void (async () => {
      try {
        const ticket = await metadataRpcClient.getDownloadTicket({
          fileId,
          disposition: 'attachment',
        });
        await Linking.openURL(ticket.url);
      } catch (error) {
        Alert.alert(
          'Could not download',
          error instanceof Error ? error.message : `Could not open ${filename}.`,
        );
      }
    })();
  };

  const cycleSort = () => {
    setSort((previous) => (previous === FileSort.NAME ? FileSort.UPDATED_AT : FileSort.NAME));
  };

  const renderRow: ListRenderItem<Item> = ({ item }) => (
    <FileRow
        item={item}
        thumbnailUrl={thumbnailUrls.get(item.itemId)}
        selecting={selecting}
        selected={Boolean(selected[item.itemId])}
        onPress={() => openItem(item)}
      onMore={() => {
          const folder = item.details.case === 'folder' ? item.details.value : undefined;
          if (folder) {
            Alert.alert(folder.name || 'Folder', undefined, [
              { text: 'Cancel', style: 'cancel' },
              {
                text: 'Open',
                onPress: () =>
                  setPath((previous) => [
                    ...previous,
                    { id: item.itemId, name: folder.name || 'Folder' },
                  ]),
              },
            ]);
            return;
          }
          const file = item.details.case === 'file' ? item.details.value : undefined;
          if (!file) return;
        Alert.alert(file.filename, describeFile(file, ' · '), [
          { text: 'Cancel', style: 'cancel' },
          { text: 'Download', onPress: () => showDownload(file.fileId, file.filename) },
        ]);
      }}
    />
  );

  const renderGridCard: ListRenderItem<Item> = ({ item }) => (
    <GridCard
      item={item}
      width={gridCardWidth}
      thumbnailUrl={thumbnailUrls.get(item.itemId)}
      selected={Boolean(selected[item.itemId])}
      selecting={selecting}
      dark={dark}
      onPress={() => openItem(item)}
    />
  );

  const isPending = contents.isPending || (root.isPending && !current);
  const canGoBack = path.length > 0;

  return (
    <YStack
      flex={1}
      bg="background">
      <YStack
        px={gutter}
        pt={insets.top + 8}
        gap={14}>
        <XStack
          items="center"
          justify="space-between">
          <XStack
            flex={1}
            minW={0}
            gap={6}
            items="center">
            {canGoBack ? (
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Go up one folder"
                hitSlop={10}
                onPress={() => setPath((previous) => previous.slice(0, -1))}>
                {({ pressed }) => (
                  <Ionicons
                    name="chevron-back"
                    size={26}
                    color={dark ? '#F3F4F6' : '#111214'}
                    style={{ marginLeft: -6, opacity: pressed ? 0.5 : 1 }}
                  />
                )}
              </Pressable>
            ) : null}
            <Text
              color="color"
              fontSize={isWide ? 34 : 30}
              fontWeight="800"
              letterSpacing={-0.8}
              numberOfLines={1}
              flex={1}>
              {title}
            </Text>
          </XStack>

          <XStack
            gap={18}
            items="center">
            {selecting ? (
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Delete selected items"
                hitSlop={10}
                disabled={Object.keys(selected).length === 0}
                onPress={runDelete}>
                {({ pressed }) => (
                  <Ionicons
                    name="trash-outline"
                    size={23}
                    color={Object.keys(selected).length === 0 ? '#9CA3AF' : '#DC2626'}
                    style={{ opacity: pressed ? 0.5 : 1 }}
                  />
                )}
              </Pressable>
            ) : (
              <>
                <Pressable
                  accessibilityRole="button"
                  accessibilityLabel="Upload"
                  hitSlop={10}
                  onPress={() => openCreateSheet()}>
                  {({ pressed }) => (
                    <MaterialCommunityIcons
                      name="tray-arrow-up"
                      size={26}
                      color={dark ? '#F3F4F6' : '#111214'}
                      style={{ opacity: pressed ? 0.5 : 1 }}
                    />
                  )}
                </Pressable>

                <Pressable
                  accessibilityRole="button"
                  accessibilityLabel="Select items"
                  hitSlop={10}
                  onPress={() => {
                    setSelected({});
                    setSelecting(true);
                  }}>
                  {({ pressed }) => (
                    <Ionicons
                      name="checkbox-outline"
                      size={24}
                      color={dark ? '#F3F4F6' : '#111214'}
                      style={{ opacity: pressed ? 0.5 : 1 }}
                    />
                  )}
                </Pressable>
              </>
            )}

            {selecting ? (
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Cancel selection"
                hitSlop={10}
                onPress={() => {
                  setSelected({});
                  setSelecting(false);
                }}>
                {({ pressed }) => (
                  <Text
                    color={LINK_BLUE}
                    fontSize={15}
                    fontWeight="600"
                    opacity={pressed ? 0.5 : 1}>
                    Done
                  </Text>
                )}
              </Pressable>
            ) : null}
          </XStack>
        </XStack>

        {selecting ? null : (
          <XStack gap={10}>
            <ActionPill
              icon="tray-arrow-up"
              label="Upload"
              onPress={() => openCreateSheet()}
            />
            <ActionPill
              icon="folder-plus-outline"
              label="Folder"
              onPress={() => openCreateSheet({ mode: 'folder' })}
            />
            <ActionPill
              icon="scan-helper"
              label="Scan"
              onPress={() => Alert.alert('Scan files', 'Scanning is coming soon.')}
            />
          </XStack>
        )}

        <XStack
          items="center"
          justify="space-between">
          <Pressable
            accessibilityRole="button"
            accessibilityLabel={`Sort by ${sort === FileSort.NAME ? 'name' : 'last modified'}`}
            hitSlop={8}
            onPress={cycleSort}>
            {({ pressed }) => (
              <XStack
                gap={6}
                items="center"
                opacity={pressed ? 0.5 : 1}>
                <Text
                  color="color"
                  fontSize={18}
                  fontWeight="700">
                  {sort === FileSort.NAME ? 'Name' : 'Modified'}
                </Text>
                <Ionicons
                  name={sort === FileSort.NAME ? 'arrow-up' : 'time-outline'}
                  size={19}
                  color={dark ? '#F3F4F6' : '#111214'}
                />
              </XStack>
            )}
          </Pressable>

          <Pressable
            accessibilityRole="button"
            accessibilityLabel={grid ? 'Show as list' : 'Show as grid'}
            hitSlop={10}
            onPress={() => setGrid((previous) => !previous)}>
            {({ pressed }) => (
              <Ionicons
                name={grid ? 'list-outline' : 'grid-outline'}
                size={22}
                color={dark ? '#F3F4F6' : '#111214'}
                style={{ opacity: pressed ? 0.5 : 1 }}
              />
            )}
          </Pressable>
        </XStack>
      </YStack>

      <YStack
        h={1}
        bg="border-color" />

      {isPending ? (
        <YStack
          px={gutter}
          pt={16}
          gap={4}>
          {Array.from({ length: 6 }, (_, index) => (
            <XStack
              key={index}
              gap={14}
              py={9}
              items="center">
              <YStack
                w={ROW_PREVIEW}
                h={ROW_PREVIEW}
                rounded={10}
                bg="gray-200 dark:gray-800"
              />
              <YStack
                flex={1}
                gap={8}>
                <YStack
                  h={12}
                  w="62%"
                  rounded={6}
                  bg="gray-200 dark:gray-800"
                />
                <YStack
                  h={10}
                  w="34%"
                  rounded={6}
                  bg="gray-200 dark:gray-800"
                />
              </YStack>
            </XStack>
          ))}
        </YStack>
      ) : contents.isError ? (
        <YStack
          flex={1}
          items="center"
          gap={10}
          py={48}>
          <Text
            color="color"
            fontSize={16}
            fontWeight="700">
            Unable to load this folder
          </Text>
          <Text
            color="gray-500 dark:gray-400"
            fontSize={13}
            text="center">
            {contents.error instanceof Error ? contents.error.message : 'Please try again.'}
          </Text>
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Try again"
            onPress={() => void contents.refetch()}>
            {({ pressed }) => (
              <XStack
                mt={4}
                px={16}
                py={10}
                rounded={12}
                borderWidth={1}
                borderColor="border-color"
                opacity={pressed ? 0.5 : 1}>
                <Text
                  color="color"
                  fontSize={14}
                  fontWeight="600">
                  Try again
                </Text>
              </XStack>
            )}
          </Pressable>
        </YStack>
      ) : (
        <FlatList
          // numColumns cannot change on a mounted list, so the key carries the
          // view mode and forces a fresh list when it toggles.
          key={grid ? `grid-${columns}` : 'list'}
          data={items}
          keyExtractor={(item: Item) => `${item.itemType}-${item.itemId}`}
          renderItem={grid ? renderGridCard : renderRow}
          numColumns={grid ? columns : 1}
          columnWrapperStyle={grid ? { gap: GRID_GAP } : undefined}
          contentContainerStyle={{
            paddingHorizontal: gutter,
            paddingTop: 8,
            paddingBottom: insets.bottom + 150,
          }}
          showsVerticalScrollIndicator={false}
          refreshControl={
            <RefreshControl
              refreshing={contents.isRefetching && !contents.isFetchingNextPage}
              onRefresh={() => void contents.refetch()}
              tintColor={dark ? '#F3F4F6' : '#111214'}
              colors={[LINK_BLUE]}
            />
          }
          onEndReachedThreshold={0.4}
          onEndReached={() => {
            if (contents.hasNextPage && !contents.isFetchingNextPage) {
              void contents.fetchNextPage();
            }
          }}
          ListEmptyComponent={
            <YStack
              items="center"
              gap={8}
              py={56}>
              <MaterialCommunityIcons
                name="folder-open-outline"
                size={30}
                color={dark ? '#9CA3AF' : '#6B7280'}
              />
              <Text
                color="color"
                fontSize={16}
                fontWeight="700">
                This folder is empty
              </Text>
              <Text
                color="gray-500 dark:gray-400"
                fontSize={13}>
                Upload or create something to fill it.
              </Text>
            </YStack>
          }
          ListFooterComponent={
            contents.isFetchingNextPage ? (
              <XStack
                justify="center"
                py={14}>
                <Spinner
                  color="gray-500"
                  size="small"
                />
              </XStack>
            ) : null
          }
        />
      )}
    </YStack>
  );
}

function itemName(item: Item): string {
  if (item.details.case === 'file') return item.details.value?.filename || 'item';
  if (item.details.case === 'folder') return item.details.value?.name || 'folder';
  return 'item';
}

function ActionPill({
  icon,
  label,
  onPress,
}: {
  icon: ComponentProps<typeof MaterialCommunityIcons>['name'];
  label: string;
  onPress: () => void;
}) {
  const dark = useColorScheme() === 'dark';

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={label}
      onPress={onPress}
      style={{ flex: 1 }}>
      {({ pressed }) => (
        <XStack
          h={52}
          gap={8}
          items="center"
          justify="center"
          rounded={14}
          bg={dark ? '#1D2027' : '#F3F4F6'}
          opacity={pressed ? 0.6 : 1}>
          <MaterialCommunityIcons
            name={icon}
            size={21}
            color={dark ? '#F3F4F6' : '#111214'}
          />
          <Text
            color="color"
            fontSize={15}
            fontWeight="600">
            {label}
          </Text>
        </XStack>
      )}
    </Pressable>
  );
}

function GridCard({
  item,
  width,
  thumbnailUrl,
  selecting,
  selected,
  dark,
  onPress,
}: {
  item: Item;
  width: number;
  thumbnailUrl?: string;
  selecting: boolean;
  selected: boolean;
  dark: boolean;
  onPress: () => void;
}) {
  const file = item.details.case === 'file' ? item.details.value : undefined;
  const folder = item.details.case === 'folder' ? item.details.value : undefined;
  const name = file?.filename || folder?.name || 'Unnamed item';
  const kind = file ? fileKind(file) : null;
  const glyph = kind ? fileKindIcon(kind) : null;
  const metadata = file ? describeFile(file, ', ') : 'Folder';

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={name}
      onPress={onPress}
      style={{ width, marginBottom: 18 }}>
      {({ pressed }) => (
        <YStack
          gap={6}
          opacity={pressed ? 0.6 : 1}>
          <Card
            h={Math.round(width * 0.78)}
            w={width}
            p={0}
            items="center"
            justify="center"
            overflow="hidden"
            rounded={12}
            bg={dark ? '#1E2128' : '#FFFFFF'}
            borderWidth={1}
            borderColor={selected ? LINK_BLUE : dark ? '#2B2F38' : '#E8EAED'}>
            {thumbnailUrl ? (
              <Image
                source={{ uri: thumbnailUrl }}
                w="100%"
                h="100%"
                objectFit="cover"
                accessibilityLabel={name}
              />
            ) : folder ? (
              <MaterialCommunityIcons
                name="folder"
                size={Math.round(width * 0.36)}
                color={FOLDER_TINT}
              />
            ) : glyph ? (
              <MaterialCommunityIcons
                name={glyph.name}
                size={Math.round(width * 0.26)}
                color={glyph.color}
              />
            ) : null}

            {selecting && selected ? (
              <YStack
                position="absolute"
                t={8}
                r={8}
                w={24}
                h={24}
                rounded={12}
                items="center"
                justify="center"
                bg={LINK_BLUE}>
                <Ionicons
                  name="checkmark"
                  size={15}
                  color="#FFFFFF"
                />
              </YStack>
            ) : null}
          </Card>

          <Text
            color="color"
            fontSize={14}
            text="start"
            numberOfLines={2}
            px={2}>
            {name}
          </Text>
          <Text
            color="gray-500 dark:gray-400"
            fontSize={12}
            text="start"
            numberOfLines={1}
            px={2}>
            {metadata}
          </Text>
        </YStack>
      )}
    </Pressable>
  );
}
