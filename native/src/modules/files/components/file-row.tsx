import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { Pressable, useColorScheme } from 'react-native';
import { Card, Image, Text, XStack, YStack } from 'tamagui';

import type { Item } from '@/gen/pb/metadata/metadata_pb';
import {
  FOLDER_TINT,
  describeFile,
  fileKind,
  fileKindIcon,
} from '@/modules/files/lib/file-display';

/** The row's preview box, matching the reference's ~72pt tiles. */
export const ROW_PREVIEW = 72;

type FileRowProps = {
  item: Item;
  thumbnailUrl?: string;
  /** Size and age of a file; folders have none in the reference design. */
  onMore: () => void;
  onPress: () => void;
  selecting?: boolean;
  selected?: boolean;
};

/**
 * One row of the folder list.
 *
 * Folders keep the reference's bare blue glyph (no tile), while files get the
 * bordered preview card. A file's second line is real metadata from the
 * service — size and when it was added — so the row is informative without
 * opening anything.
 */
export function FileRow({
  item,
  thumbnailUrl,
  onMore,
  onPress,
  selecting = false,
  selected = false,
}: FileRowProps) {
  const dark = useColorScheme() === 'dark';
  const file = item.details.case === 'file' ? item.details.value : undefined;
  const folder = item.details.case === 'folder' ? item.details.value : undefined;

  const name = file?.filename || folder?.name || 'Unnamed item';
  const kind = file ? fileKind(file) : null;
  const glyph = kind ? fileKindIcon(kind) : null;
  // Any ready thumbnail wins, not just images: the preview worker renders PDF
  // pages too, and that is what the reference shows for documents.
  const showThumbnail = Boolean(thumbnailUrl);
  const metadata = file ? describeFile(file, ', ') : '';

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={name}
      accessibilityState={{ selected: selecting ? selected : undefined }}
      onPress={onPress}>
      {({ pressed }) => (
        <XStack
          py={9}
          gap={14}
          items="center"
          opacity={pressed ? 0.6 : 1}>
          {selecting ? (
            <YStack
              w={26}
              h={26}
              rounded={13}
              items="center"
              justify="center"
              borderWidth={1.5}
              borderColor={selected ? '#1A73E8' : dark ? '#4B5563' : '#9CA3AF'}
              bg={selected ? '#1A73E8' : 'transparent'}>
              {selected ? (
                <Ionicons
                  name="checkmark"
                  size={16}
                  color="#FFFFFF"
                />
              ) : null}
            </YStack>
          ) : null}

          {folder ? (
            <YStack
              w={ROW_PREVIEW}
              h={ROW_PREVIEW}
              items="center"
              justify="center">
              <MaterialCommunityIcons
                name="folder"
                size={52}
                color={FOLDER_TINT}
              />
            </YStack>
          ) : (
            <Card
              w={ROW_PREVIEW}
              h={ROW_PREVIEW}
              p={0}
              items="center"
              justify="center"
              overflow="hidden"
              rounded={10}
              bg={dark ? '#1E2128' : '#FFFFFF'}
              borderWidth={1}
              borderColor={dark ? '#2B2F38' : '#E8EAED'}>
              {showThumbnail && thumbnailUrl ? (
                <Image
                  source={{ uri: thumbnailUrl }}
                  w="100%"
                  h="100%"
                  objectFit="cover"
                  accessibilityLabel={name}
                />
              ) : glyph ? (
                <MaterialCommunityIcons
                  name={glyph.name}
                  size={30}
                  color={glyph.color}
                />
              ) : null}
            </Card>
          )}

          <YStack
            flex={1}
            minW={0}
            justify="center"
            gap={2}>
            <Text
              color="color"
              fontSize={17}
              // Explicit alignment: the row is a Pressable, and a pressable's
              // own text alignment otherwise leaks into the label on web.
              text="start"
              numberOfLines={2}>
              {name}
            </Text>
            {metadata ? (
              <Text
                color="gray-500 dark:gray-400"
                fontSize={13}
                text="start"
                numberOfLines={1}>
                {metadata}
              </Text>
            ) : null}
          </YStack>

          {selecting ? null : (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel={`Actions for ${name}`}
              hitSlop={12}
              onPress={onMore}>
              {({ pressed: morePressed }) => (
                <YStack
                  w={28}
                  h={36}
                  items="center"
                  justify="center"
                  opacity={morePressed ? 0.5 : 1}>
                  <Ionicons
                    name="ellipsis-vertical"
                    size={20}
                    color={dark ? '#9AA0A6' : '#3C4043'}
                  />
                </YStack>
              )}
            </Pressable>
          )}
        </XStack>
      )}
    </Pressable>
  );
}
