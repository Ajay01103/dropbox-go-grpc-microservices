import { Ionicons } from '@expo/vector-icons';
import { Pressable } from 'react-native';
import { Button, Spinner, Text, XStack, YStack } from 'tamagui';

import { authColors } from './auth-screen';

/**
 * The controls shared by the two auth screens, each one a direct transcription
 * of a piece of the reference design: a black pill CTA, a line-and-label
 * divider, a pair of white social buttons, the remember-me checkbox, and the
 * footer prompt that swaps between the screens.
 */

export function AuthPrimaryButton({
  label,
  loading = false,
  disabled = false,
  onPress,
}: {
  label: string;
  loading?: boolean;
  disabled?: boolean;
  onPress: () => void;
}) {
  return (
    <Button
      accessibilityRole="button"
      borderWidth={0}
      bg="#0B0B0C"
      disabled={disabled || loading}
      h="14"
      opacity="1 disabled:0.55"
      px="5"
      rounded="2xl"
      onPress={onPress}>
      <XStack gap="2" items="center" justify="center">
        {loading ? <Spinner color="#FFFFFF" size="small" /> : null}
        <Text color="#FFFFFF" fontSize="4" fontWeight="700">
          {loading ? 'Please wait…' : label}
        </Text>
      </XStack>
    </Button>
  );
}

export function AuthDivider({ label }: { label: string }) {
  return (
    <XStack gap="3" items="center">
      <YStack bg="rgba(17, 24, 39, 0.12)" flex={1} h={1} />
      <Text color={authColors.textMuted} fontSize="3">
        {label}
      </Text>
      <YStack bg="rgba(17, 24, 39, 0.12)" flex={1} h={1} />
    </XStack>
  );
}

/**
 * The two provider buttons, side by side.
 *
 * They are placeholders for a real OAuth flow and say so when pressed, rather
 * than starting a flow that would dead-end. The layout is responsive on its
 * own: both buttons share the row with `flex={1}` and their labels shrink, so a
 * 320px-wide phone wraps the text instead of clipping it.
 */
export function AuthSocialRow({ onUnavailable }: { onUnavailable: (provider: string) => void }) {
  return (
    <XStack gap="3">
      <SocialButton
        icon="logo-google"
        label="Sign in Google"
        onPress={() => onUnavailable('Google')}
      />
      <SocialButton
        icon="logo-apple"
        label="Sign in Apple"
        onPress={() => onUnavailable('Apple')}
      />
    </XStack>
  );
}

function SocialButton({
  icon,
  label,
  onPress,
}: {
  icon: keyof typeof Ionicons.glyphMap;
  label: string;
  onPress: () => void;
}) {
  return (
    <Button
      accessibilityRole="button"
      bg="#FFFFFF"
      borderColor="rgba(17, 24, 39, 0.08)"
      borderWidth={1}
      flex={1}
      gap="2"
      h="12"
      justify="center"
      minW={0}
      opacity="1 press:0.85"
      px="3"
      rounded="2xl"
      onPress={onPress}>
      <Ionicons color="#0B0B0C" name={icon} size={18} />
      <Text color="#0B0B0C" fontSize="3" fontWeight="600" numberOfLines={1}>
        {label}
      </Text>
    </Button>
  );
}

/**
 * A checkmark box that needs no native control: the whole row is pressable and
 * the box mirrors the reference's small rounded square.
 */
export function AuthRememberRow({
  checked,
  label,
  onChange,
  trailing,
}: {
  checked: boolean;
  label: string;
  onChange: (checked: boolean) => void;
  /** Right-aligned action, e.g. "Forgot password?". */
  trailing?: { label: string; onPress: () => void };
}) {
  return (
    <XStack items="center" justify="space-between">
      <Pressable
        accessibilityLabel={label}
        accessibilityRole="checkbox"
        accessibilityState={{ checked }}
        hitSlop={8}
        onPress={() => onChange(!checked)}>
        <XStack gap="2" items="center">
          <YStack
            bg={checked ? '#0B0B0C' : 'transparent'}
            borderColor={checked ? '#0B0B0C' : 'rgba(17, 24, 39, 0.28)'}
            borderWidth={1}
            h="4"
            items="center"
            justify="center"
            rounded="xs"
            w="4">
            {checked ? <Ionicons color="#FFFFFF" name="checkmark" size={12} /> : null}
          </YStack>
          <Text color={authColors.textMuted} fontSize="3">
            {label}
          </Text>
        </XStack>
      </Pressable>

      {trailing ? (
        <Pressable
          accessibilityRole="link"
          hitSlop={8}
          onPress={trailing.onPress}>
          <Text color="#2563EB" fontSize="3" fontWeight="600">
            {trailing.label}
          </Text>
        </Pressable>
      ) : null}
    </XStack>
  );
}

/** "Didn't have an account? Create an account" and its mirror. */
export function AuthFooterPrompt({
  actionLabel,
  onPress,
  question,
}: {
  actionLabel: string;
  onPress: () => void;
  question: string;
}) {
  return (
    <XStack flexWrap="wrap" gap="1" items="center" justify="center" pt="1">
      <Text color={authColors.textMuted} fontSize="3">
        {question}
      </Text>
      <Pressable accessibilityRole="link" hitSlop={8} onPress={onPress}>
        <Text color="#0B0B0C" fontSize="3" fontWeight="700">
          {actionLabel}
        </Text>
      </Pressable>
    </XStack>
  );
}
