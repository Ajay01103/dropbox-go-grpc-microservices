import { Carousel } from 'react-native-reanimated-carousel';

import type { Item } from '@/gen/pb/metadata/metadata_pb';
import { CARD_CAPTION_HEIGHT, ItemCard, previewHeight } from './item-card';

const CARD_GAP = 12;

type ItemCarouselProps = {
  items: Item[];
  /** Width of the carousel viewport, already inset by the screen gutter. */
  width: number;
  cardWidth: number;
  thumbnailUrls: ReadonlyMap<string, string>;
  labels: ReadonlyMap<string, string>;
  onMore: (item: Item) => void;
};

/**
 * A horizontal carousel of recent items.
 *
 * The page distance is `cardWidth + gap`, which is shorter than the viewport:
 * that is what shows a strip of the next card instead of a hard cut, matching
 * the reference's peek. Cards snap one per page.
 *
 * Recent is split across two of these rather than one long strip (see
 * `home-view`), so a second deck starts at the top of the older half instead of
 * forcing a long swipe through everything.
 */
export function ItemCarousel({
  items,
  width,
  cardWidth,
  thumbnailUrls,
  labels,
  onMore,
}: ItemCarouselProps) {
  return (
    <Carousel
      data={items}
      keyExtractor={(item: Item) => `${item.itemType}-${item.itemId}`}
      style={{ width, height: previewHeight(cardWidth) + CARD_CAPTION_HEIGHT }}
      itemSize={cardWidth + CARD_GAP}
      snapMode="page"
      loop={false}
      overscrollEnabled
      renderItem={({ item }: { item: Item }) => (
        <ItemCard
          item={item}
          width={cardWidth}
          thumbnailUrl={thumbnailUrls.get(item.itemId)}
          location={labels.get(item.folderId) ?? 'Dropbox'}
          onMore={() => onMore(item)}
        />
      )}
    />
  );
}
