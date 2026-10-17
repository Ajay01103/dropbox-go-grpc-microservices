import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { useMutation } from '@tanstack/react-query';
import { useRouter } from 'expo-router';
import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  Linking,
  Pressable,
  RefreshControl,
  StyleSheet,
  useColorScheme,
  useWindowDimensions,
} from 'react-native';
import Animated, {
  Extrapolation,
  interpolate,
  useAnimatedScrollHandler,
  useAnimatedStyle,
  type SharedValue,
} from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { Spinner, Text, XStack, YStack } from 'tamagui';

import type { Item } from '@/gen/pb/metadata/metadata_pb';
import { useTabScrollY } from '@/components/tab-scroll';
import { useFolderLabels, useRecentItems, useThumbnailUrls } from '@/modules/files/api/use-files';
import { formatBytes, formatRelativeTime } from '@/modules/files/lib/file-display';
import { metadataRpcClient } from '@/lib/rpc';
import { useCreateSheet } from '@/modules/upload/components/create-sheet';
import { ItemCarousel } from '../components/item-carousel';
import { CARD_CAPTION_HEIGHT, previewHeight } from '../components/item-card';

const LINK_BLUE = '#1A73E8';
const CONTENT_MAX_WIDTH = 880;
const TABLET_MIN_WIDTH = 768;
const NOTICE_MS = 2400;

/** Height of the title/icons row, below the status bar. */
const HEADER_ROW_HEIGHT = 58;
/** How far the list scrolls before the title has fully collapsed. */
const COLLAPSE_DISTANCE = 72;

/**
 * Home is the account-wide recent feed, split across two carousels.
 *
 * The newest half lives in the "Recent" deck and the rest in "Earlier": one
 * strip long enough to hold a real account would be a long swipe, so the second
 * deck starts where the first stops. Everything comes from `use-files` — one
 * recent page list, one batched thumbnail presign, one breadcrumb lookup per
 * distinct parent folder — and each card carries the size and age that a row
 * subtitle would otherwise hide.
 *
 * The header is pinned: only its title collapses with the scroll, so Home, the
 * bell and upload stay reachable. The same offset collapses the tab bar's
 * search field (see `tab-scroll`).
 */
export default function HomeView() {
  const insets = useSafeAreaInsets();
  const router = useRouter();
  const dark = useColorScheme() === 'dark';
  const { width } = useWindowDimensions();
  const { openCreateSheet } = useCreateSheet();
  const scrollY = useTabScrollY();

  const isWide = width >= TABLET_MIN_WIDTH;
  const gutter = isWide ? 28 : 16;
  const contentWidth = Math.min(width, CONTENT_MAX_WIDTH) - gutter * 2;
  const cardWidth = Math.round(Math.min(Math.max(contentWidth * 0.76, 240), 340));

  const recent = useRecentItems();
  const [notice, setNotice] = useState<string | null>(null);

  // A feed that is paginated can repeat a row when something is touched
  // between requests; dedupe before splitting it across the decks.
  const items = useMemo(() => {
    const seen = new Set<string>();
    const unique: Item[] = [];
    for (const page of recent.data?.pages ?? []) {
      for (const item of page.items) {
        const key = `${item.itemType}-${item.itemId}`;
        if (seen.has(key)) continue;
        seen.add(key);
        unique.push(item);
      }
    }
    return unique;
  }, [recent.data]);

  // Newest half in the first deck, the rest in the second. An even split keeps
  // both decks populated on small accounts instead of leaving the second one
  // empty until enough items exist.
  const splitAt = Math.max(1, Math.ceil(items.length / 2));
  const leadItems = items.slice(0, splitAt);
  const trailItems = items.slice(splitAt);

  const fileIds = useMemo(
    () => items.filter((item) => item.details.case === 'file').map((item) => item.itemId),
    [items],
  );
  const { urls: thumbnailUrls } = useThumbnailUrls(fileIds);
  const labels = useFolderLabels(useMemo(() => items.map((item) => item.folderId), [items]));

  useEffect(() => {
    if (!notice) return;
    const timer = setTimeout(() => setNotice(null), NOTICE_MS);
    return () => clearTimeout(timer);
  }, [notice]);

  const onScroll = useAnimatedScrollHandler({
    onScroll: (event) => {
      scrollY.set(event.contentOffset.y);
    },
  });

  const download = useMutation({
    mutationFn: async (fileId: string) => {
      // The metadata service mints the ticket after its ownership check; the
      // bytes never travel through our API.
      const ticket = await metadataRpcClient.getDownloadTicket({
        fileId,
        disposition: 'attachment',
      });
      await Linking.openURL(ticket.url);
    },
    onError: (error) => {
      Alert.alert(
        'Could not download',
        error instanceof Error ? error.message : 'Please try again.',
      );
    },
  });

  const showItemActions = (item: Item) => {
    const file = item.details.case === 'file' ? item.details.value : undefined;
    const folder = item.details.case === 'folder' ? item.details.value : undefined;

    if (folder) {
      Alert.alert(folder.name || 'Folder', labels.get(item.folderId) ?? 'Dropbox', [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Open Files', onPress: () => router.navigate('/files') },
      ]);
      return;
    }
    if (!file) return;

    Alert.alert(
      file.filename,
      [formatBytes(file.sizeBytes), formatRelativeTime(file.createdAt)]
        .filter(Boolean)
        .join(' · '),
      [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Download', onPress: () => download.mutate(file.fileId) },
      ],
    );
  };

  const sectionHeader = (title: string, showSeeAll: boolean) => (
    <XStack
      items="center"
      justify="space-between"
      pb={12}>
      <XStack
        gap={8}
        items="center">
        <MaterialCommunityIcons
          name={title === 'Recent' ? 'clock' : 'history'}
          size={21}
          color={dark ? '#F3F4F6' : '#111214'}
        />
        <Text
          color="color"
          fontSize={21}
          fontWeight="700">
          {title}
        </Text>
      </XStack>

      {showSeeAll ? (
        <Pressable
          accessibilityRole="button"
          accessibilityLabel="See all recent items"
          hitSlop={8}
          onPress={() => router.navigate('/files')}>
          {({ pressed }) => (
            <Text
              color={LINK_BLUE}
              fontSize={15}
              fontWeight="600"
              opacity={pressed ? 0.5 : 1}>
              See all
            </Text>
          )}
        </Pressable>
      ) : null}
    </XStack>
  );

  const carouselSkeletons = (
    <XStack gap={12}>
      {Array.from({ length: 2 }, (_, index) => (
        <YStack
          key={index}
          gap={8}>
          <YStack
            w={cardWidth}
            h={previewHeight(cardWidth)}
            rounded={14}
            bg="gray-200 dark:gray-800"
          />
          <YStack
            h={CARD_CAPTION_HEIGHT}
            gap={8}
            pt={4}>
            <YStack
              h={12}
              w={cardWidth * 0.7}
              rounded={6}
              bg="gray-200 dark:gray-800"
            />
            <YStack
              h={10}
              w={cardWidth * 0.4}
              rounded={6}
              bg="gray-200 dark:gray-800"
            />
          </YStack>
        </YStack>
      ))}
    </XStack>
  );

  return (
    <YStack
      flex={1}
      bg="background">
      <HomeHeader
        dark={dark}
        gutter={gutter}
        insetsTop={insets.top}
        isWide={isWide}
        scrollY={scrollY}
        onBell={() => setNotice('You’re all caught up')}
        onUpload={() => openCreateSheet()}
      />

      <Animated.ScrollView
        onScroll={onScroll}
        scrollEventThrottle={16}
        showsVerticalScrollIndicator={false}
        keyboardShouldPersistTaps="handled"
        refreshControl={
          <RefreshControl
            refreshing={recent.isRefetching && !recent.isFetchingNextPage}
            onRefresh={() => void recent.refetch()}
            tintColor={dark ? '#F3F4F6' : '#111214'}
            colors={[LINK_BLUE]}
          />
        }
        style={{ width: '100%', maxWidth: CONTENT_MAX_WIDTH, alignSelf: 'center' }}
        contentContainerStyle={{
          paddingHorizontal: gutter,
          // The tab bar (and its search field) floats over the list.
          paddingBottom: insets.bottom + 150,
        }}>
        {notice ? (
          <XStack
            self="flex-end"
            mt={8}
            px={14}
            py={8}
            rounded={999}
            bg="gray-100 dark:gray-800">
            <Text
              color="gray-700 dark:gray-200"
              fontSize={13}
              fontWeight="600">
              {notice}
            </Text>
          </XStack>
        ) : null}

        <YStack pt={6}>
          {sectionHeader('Recent', true)}

          {recent.isPending ? (
            carouselSkeletons
          ) : recent.isError ? (
            <YStack
              items="center"
              gap={10}
              py={36}>
              <Text
                color="color"
                fontSize={16}
                fontWeight="700">
                Unable to load recent items
              </Text>
              <Text
                color="gray-500 dark:gray-400"
                fontSize={13}
                text="center">
                {recent.error instanceof Error ? recent.error.message : 'Please try again.'}
              </Text>
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Try again"
                onPress={() => void recent.refetch()}>
                {({ pressed }) => (
                  <XStack
                    mt={4}
                    px={16}
                    py={10}
                    gap={8}
                    items="center"
                    rounded={12}
                    borderWidth={1}
                    borderColor="border-color"
                    opacity={pressed ? 0.5 : 1}>
                    <Ionicons
                      name="refresh"
                      size={16}
                      color={dark ? '#F3F4F6' : '#111214'}
                    />
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
          ) : items.length === 0 ? (
            <EmptyRecent onCreate={() => openCreateSheet()} dark={dark} />
          ) : (
            <ItemCarousel
              items={leadItems}
              width={contentWidth}
              cardWidth={cardWidth}
              thumbnailUrls={thumbnailUrls}
              labels={labels}
              onMore={showItemActions}
            />
          )}
        </YStack>

        {trailItems.length > 0 ? (
          <YStack mt={26}>
            {sectionHeader('Earlier', false)}
            <ItemCarousel
              items={trailItems}
              width={contentWidth}
              cardWidth={cardWidth}
              thumbnailUrls={thumbnailUrls}
              labels={labels}
              onMore={showItemActions}
            />
          </YStack>
        ) : null}

        {recent.hasNextPage ? (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Load more recent items"
            disabled={recent.isFetchingNextPage}
            onPress={() => void recent.fetchNextPage()}>
            {({ pressed }) => (
              <XStack
                self="center"
                mt={22}
                px={18}
                py={10}
                gap={8}
                items="center"
                rounded={999}
                borderWidth={1}
                borderColor="border-color"
                opacity={pressed || recent.isFetchingNextPage ? 0.5 : 1}>
                {recent.isFetchingNextPage ? (
                  <Spinner
                    color="gray-500"
                    size="small"
                  />
                ) : null}
                <Text
                  color="color"
                  fontSize={14}
                  fontWeight="600">
                  {recent.isFetchingNextPage ? 'Loading…' : 'Load more'}
                </Text>
              </XStack>
            )}
          </Pressable>
        ) : null}

        {/* Starred has no backend yet; the section is present so the feed keeps
            its shape, and it honestly says what will fill it. */}
        <YStack
          gap={10}
          mt={30}
          pt={18}
          borderTopWidth={1}
          borderColor="border-color">
          <XStack
            items="center"
            justify="space-between">
            <XStack
              gap={8}
              items="center">
              <MaterialCommunityIcons
                name="star-outline"
                size={22}
                color={dark ? '#F3F4F6' : '#111214'}
              />
              <Text
                color="color"
                fontSize={21}
                fontWeight="700">
                Starred
              </Text>
            </XStack>

            <Pressable
              accessibilityRole="button"
              accessibilityLabel="See all starred items"
              hitSlop={8}
              onPress={() => router.navigate('/files')}>
              {({ pressed }) => (
                <Text
                  color={LINK_BLUE}
                  fontSize={15}
                  fontWeight="600"
                  opacity={pressed ? 0.5 : 1}>
                  See all
                </Text>
              )}
            </Pressable>
          </XStack>

          <Text
            color="gray-500 dark:gray-400"
            fontSize={14}>
            Anything you star shows up here.
          </Text>
        </YStack>
      </Animated.ScrollView>
    </YStack>
  );
}

function EmptyRecent({ onCreate, dark }: { onCreate: () => void; dark: boolean }) {
  return (
    <YStack
      items="center"
      gap={8}
      py={48}>
      <YStack
        w={62}
        h={62}
        rounded={31}
        items="center"
        justify="center"
        bg="gray-100 dark:gray-800">
        <MaterialCommunityIcons
          name="clock-outline"
          size={26}
          color={dark ? '#D1D5DB' : '#6B7280'}
        />
      </YStack>
      <Text
        color="color"
        fontSize={16}
        fontWeight="700">
        No recent items yet
      </Text>
      <Text
        color="gray-500 dark:gray-400"
        fontSize={13}
        text="center"
        maxW={280}>
        Files and folders you create, upload, or open will show up here.
      </Text>
      <Pressable
        accessibilityRole="button"
        accessibilityLabel="Create something"
        onPress={onCreate}>
        {({ pressed }) => (
          <XStack
            mt={6}
            px={18}
            py={11}
            rounded={999}
            bg={LINK_BLUE}
            opacity={pressed ? 0.7 : 1}>
            <Text
              color="#FFFFFF"
              fontSize={14}
              fontWeight="700">
              Create something
            </Text>
          </XStack>
        )}
      </Pressable>
    </YStack>
  );
}

type HomeHeaderProps = {
  dark: boolean;
  gutter: number;
  insetsTop: number;
  isWide: boolean;
  scrollY: SharedValue<number>;
  onBell: () => void;
  onUpload: () => void;
};

/**
 * The pinned header.
 *
 * It is a normal (non-absolute) child of the screen, so it never scrolls away;
 * only the title scales down and the surface fades in. The icons keep their
 * size and position throughout, which is what makes Home, notifications and
 * upload permanently reachable while the feed scrolls.
 */
function HomeHeader({ dark, gutter, insetsTop, isWide, scrollY, onBell, onUpload }: HomeHeaderProps) {
  const ink = dark ? '#F3F4F6' : '#111214';

  const surfaceStyle = useAnimatedStyle(() => {
    const progress = interpolate(scrollY.value, [0, 24], [0, 1], Extrapolation.CLAMP);
    return {
      backgroundColor: dark ? `rgba(11, 15, 25, ${progress})` : `rgba(255, 255, 255, ${progress})`,
    };
  });

  const borderStyle = useAnimatedStyle(() => ({
    opacity: interpolate(scrollY.value, [0, 24], [0, 1], Extrapolation.CLAMP),
  }));

  const titleStyle = useAnimatedStyle(() => {
    const progress = interpolate(
      scrollY.value,
      [0, COLLAPSE_DISTANCE],
      [0, 1],
      Extrapolation.CLAMP,
    );
    return {
      transform: [{ scale: 1 - progress * 0.44 }, { translateY: progress * 2 }],
    };
  });

  return (
    <Animated.View style={[{ paddingTop: insetsTop }, surfaceStyle]}>
      <XStack
        h={HEADER_ROW_HEIGHT}
        px={gutter}
        items="center"
        justify="space-between"
        style={styles.headerRow}>
        <Animated.View style={[styles.titleWrap, titleStyle]}>
          <Text
            color="color"
            fontSize={isWide ? 36 : 32}
            fontWeight="800"
            letterSpacing={-0.9}
            numberOfLines={1}>
            Home
          </Text>
        </Animated.View>

        <XStack
          gap={isWide ? 22 : 18}
          items="center">
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Notifications"
            hitSlop={10}
            onPress={onBell}>
            {({ pressed }) => (
              <Ionicons
                name="notifications-outline"
                size={24}
                color={ink}
                style={{ opacity: pressed ? 0.5 : 1 }}
              />
            )}
          </Pressable>

          <Pressable
            accessibilityRole="button"
            accessibilityLabel="Upload"
            hitSlop={10}
            onPress={onUpload}>
            {({ pressed }) => (
              <MaterialCommunityIcons
                name="tray-arrow-up"
                size={26}
                color={ink}
                style={{ opacity: pressed ? 0.5 : 1 }}
              />
            )}
          </Pressable>
        </XStack>
      </XStack>

      <Animated.View
        style={[
          styles.hairline,
          { borderBottomColor: dark ? '#242832' : '#EBEDF0' },
          borderStyle,
        ]}
      />
    </Animated.View>
  );
}

const styles = StyleSheet.create({
  headerRow: {
    width: '100%',
    maxWidth: CONTENT_MAX_WIDTH,
    alignSelf: 'center',
  },
  titleWrap: {
    // Scaling from the left edge keeps the title anchored where the design puts
    // it while it shrinks, instead of drifting toward the middle of the bar.
    transformOrigin: 'left center',
  },
  hairline: {
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
});
