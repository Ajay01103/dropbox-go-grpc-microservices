import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import type { ReactNode } from 'react';
import {
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  useWindowDimensions,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { Text, XStack, YStack } from 'tamagui';

/**
 * The shared shell for the two auth screens, and the one place their layout is
 * decided.
 *
 * Everything here is tuned for smaller screens first and widened for tablets:
 * the form column is a fixed max width, so a phone gets edge-to-edge padding
 * while an iPad or a landscape phone gets a centred column instead of a
 * stretched form. The screen scrolls, so a short device (or an open keyboard)
 * never hides the submit button — which is the single most common way a login
 * screen breaks on a small phone.
 *
 * The palette is the reference design's: a pale pink → cream → white wash, with
 * whites and near-blacks layered on top. It is intentionally not theme-aware:
 * the design is a light one, and matching it in both colour schemes is more
 * faithful than tinting half of it.
 */

const GRADIENT = ['#F6E5F4', '#FBEFEA', '#FBF6E7', '#FFFFFF'] as const;
const TEXT_STRONG = '#0B0B0C';
const TEXT_MUTED = '#6B7280';
const SURFACE = 'rgba(255, 255, 255, 0.72)';
const SURFACE_BORDER = 'rgba(255, 255, 255, 0.95)';

/** Widest the form column ever gets; tablets centre it instead of stretching. */
const FORM_MAX_WIDTH = 440;

export function AuthScreen({
  children,
  onBack,
  showBack = false,
}: {
  children: ReactNode;
  /** Called by the round back button. */
  onBack?: () => void;
  showBack?: boolean;
}) {
  const insets = useSafeAreaInsets();
  const { height } = useWindowDimensions();

  // Short screens (a small phone in landscape, or one with the keyboard open)
  // need tighter vertical rhythm or the footer link scrolls out of reach.
  const compact = height < 700;

  return (
    <LinearGradient colors={[...GRADIENT]} end={{ x: 0.35, y: 1 }} start={{ x: 0, y: 0 }} style={styles.fill}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={styles.fill}>
        {showBack && onBack ? (
          <Pressable
            accessibilityLabel="Go back"
            accessibilityRole="button"
            hitSlop={8}
            onPress={onBack}
            style={[styles.backButton, { top: insets.top + 8 }]}>
            <Ionicons color={TEXT_STRONG} name="chevron-back" size={22} />
          </Pressable>
        ) : null}

        <ScrollView
          alwaysBounceVertical={false}
          contentContainerStyle={[
            styles.scrollContent,
            {
              paddingTop: insets.top + (showBack ? 56 : 24),
              paddingBottom: insets.bottom + 24,
            },
          ]}
          keyboardDismissMode="on-drag"
          keyboardShouldPersistTaps="handled">
          <YStack
            gap={compact ? '5' : '6'}
            maxW={FORM_MAX_WIDTH}
            self="center"
            w="100%"
            px="5 sm:8">
            {children}
          </YStack>
        </ScrollView>
      </KeyboardAvoidingView>
    </LinearGradient>
  );
}

/** Centred title + supporting copy, as in the reference. */
export function AuthHeading({ subtitle, title }: { subtitle: string; title: string }) {
  return (
    <YStack gap="2" items="center">
      <Text color={TEXT_STRONG} fontSize="6" fontWeight="800" text="center">
        {title}
      </Text>
      <Text color={TEXT_MUTED} fontSize="3" text="center">
        {subtitle}
      </Text>
    </YStack>
  );
}

/** A row of fields, so the gap between them is decided once. */
export function AuthForm({ children }: { children: ReactNode }) {
  return <YStack gap="4">{children}</YStack>;
}

export function AuthErrorBanner({ message }: { message: string | null }) {
  if (!message) return null;

  return (
    <XStack
      accessibilityLiveRegion="polite"
      accessibilityRole="alert"
      gap="2"
      items="center"
      px="3"
      py="2"
      rounded="xl"
      bg="rgba(220, 38, 38, 0.08)"
      borderColor="rgba(220, 38, 38, 0.25)"
      borderWidth={1}>
      <Ionicons color="#B91C1C" name="alert-circle-outline" size={18} />
      <Text color="#991B1B" flex={1} fontSize="3">
        {message}
      </Text>
    </XStack>
  );
}

export const authColors = {
  surface: SURFACE,
  surfaceBorder: SURFACE_BORDER,
  textStrong: TEXT_STRONG,
  textMuted: TEXT_MUTED,
} as const;

const styles = StyleSheet.create({
  fill: {
    flex: 1,
  },
  backButton: {
    alignItems: 'center',
    backgroundColor: 'rgba(255, 255, 255, 0.7)',
    borderRadius: 999,
    height: 40,
    justifyContent: 'center',
    left: 20,
    position: 'absolute',
    width: 40,
    zIndex: 2,
  },
  scrollContent: {
    flexGrow: 1,
    justifyContent: 'center',
  },
});
