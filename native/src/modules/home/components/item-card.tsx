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

/** Height reserved for the three text lines under a preview. */
export const CARD_CAPTION_HEIGHT = 88;

/** The preview is 72% as tall as the card is wide, like the reference tiles. */
export const previewHeight = (cardWidth: number) => Math.round(cardWidth * 0.72);

type ItemCardProps = {
  item: Item;
  thumbnailUrl?: string;
  /** "Dropbox › some" */
  location: string;
  width: number;
  onMore: () => void;
};

/**
 * One card in the recent carousels.
 *
 * The card is deliberately taller than a row: the whole point of the carousel
 * is to show the preview, the name, where the item lives *and* its size/age at
 * a glance, so the metadata gets its own line instead of being appended to the
 * location string.
 */
export function ItemCard({ item, thumbnailUrl, location, width, onMore }: ItemCardProps) {
  const dark = useColorScheme() === 'dark';
  const file = item.details.case === 'file' ? item.details.value : undefined;
  const folder = item.details.case === 'folder' ? item.details.value : undefined;

  const name = file?.filename || folder?.name || 'Unnamed item';
  const kind = file ? fileKind(file) : null;
  const glyph = kind ? fileKindIcon(kind) : null;
  // Any ready thumbnail wins, not just images: the preview worker renders PDF
  // pages too, and that is what the reference shows for documents.
  const showThumbnail = Boolean(thumbnailUrl);
  const metadata = file ? describeFile(file) : 'Folder';

  return (
    <YStack
      w={width}
      gap={6}>
      <Card
        h={previewHeight(width)}
        p={0}
        items="center"
        justify="center"
        overflow="hidden"
        rounded={14}
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
        ) : folder ? (
          <MaterialCommunityIcons
            name="folder"
            size={Math.round(width * 0.34)}
            color={FOLDER_TINT}
          />
        ) : glyph ? (
          <MaterialCommunityIcons
            name={glyph.name}
            size={Math.round(width * 0.24)}
            color={glyph.color}
          />
        ) : null}

        <Pressable
          accessibilityRole="button"
          accessibilityLabel={`Actions for ${name}`}
          hitSlop={10}
          onPress={onMore}
          style={{ position: 'absolute', top: 6, right: 6 }}>
          {({ pressed }) => (
            <XStack
              w={30}
              h={30}
              items="center"
              justify="center"
              rounded={15}
              bg={dark ? 'rgba(17,19,24,0.55)' : 'rgba(255,255,255,0.82)'}
              opacity={pressed ? 0.6 : 1}>
              <Ionicons
                name="ellipsis-vertical"
                size={17}
                color={dark ? '#F3F4F6' : '#3C4043'}
              />
            </XStack>
          )}
        </Pressable>
      </Card>

      <YStack
        h={CARD_CAPTION_HEIGHT}
        gap={1}
        px={2}>
        <Text
          color="color"
          fontSize={15}
          text="start"
          numberOfLines={2}>
          {name}
        </Text>
        <Text
          color="gray-500 dark:gray-400"
          fontSize={12}
          text="start"
          numberOfLines={1}>
          {location}
        </Text>
        <Text
          color="gray-500 dark:gray-400"
          fontSize={12}
          text="start"
          numberOfLines={1}>
          {metadata}
        </Text>
      </YStack>
    </YStack>
  );
}
