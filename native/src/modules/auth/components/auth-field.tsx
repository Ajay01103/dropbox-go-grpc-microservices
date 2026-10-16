import { Ionicons } from '@expo/vector-icons';
import { useState } from 'react';
import { Pressable } from 'react-native';
import { Input, Text, XStack, YStack } from 'tamagui';

import { authColors } from './auth-screen';

/**
 * One labelled field of an auth form: label, icon-prefixed input, optional
 * password reveal, and a field-level error.
 *
 * The input is composed rather than configured — a styled row wraps a borderless
 * `Input` — because the reference design puts the icon inside the same rounded
 * surface as the text, which a plain Input with `icon=` cannot express.
 *
 * Focus is tracked in state instead of with a style clause: on native the
 * keyboard's focus events are reliable, and it keeps the focused look identical
 * across iOS and Android.
 */
export function AuthField({
  autoComplete,
  error,
  icon,
  keyboardType,
  label,
  onBlur,
  onChangeText,
  onSubmitEditing,
  placeholder,
  returnKeyType,
  secureTextEntry = false,
  value,
}: {
  autoComplete?: 'email' | 'name' | 'new-password' | 'current-password' | 'off';
  /** Field-level message; also decides the red border. */
  error?: string | null;
  icon: keyof typeof Ionicons.glyphMap;
  keyboardType?: 'default' | 'email-address';
  label: string;
  onBlur?: () => void;
  onChangeText: (value: string) => void;
  onSubmitEditing?: () => void;
  placeholder: string;
  returnKeyType?: 'done' | 'go' | 'next';
  secureTextEntry?: boolean;
  value: string;
}) {
  const [focused, setFocused] = useState(false);
  const [revealed, setRevealed] = useState(false);

  const borderColor = error
    ? 'rgba(220, 38, 38, 0.55)'
    : focused
      ? 'rgba(17, 24, 39, 0.35)'
      : authColors.surfaceBorder;

  return (
    <YStack gap="2">
      <Text color={authColors.textStrong} fontSize="3" fontWeight="600">
        {label}
      </Text>

      <XStack
        bg={authColors.surface}
        borderColor={borderColor}
        borderWidth={1}
        gap="2"
        h="12"
        items="center"
        px="4"
        rounded="2xl"
        shadowColor="#0B0B0C"
        shadowOffset={{ height: 4, width: 0 }}
        shadowOpacity={0.05}
        shadowRadius={12}>
        <Ionicons color={focused ? authColors.textStrong : '#9CA3AF'} name={icon} size={18} />

        <Input
          accessibilityLabel={label}
          autoCapitalize={keyboardType === 'email-address' ? 'none' : 'words'}
          autoComplete={autoComplete}
          autoCorrect={keyboardType !== 'email-address'}
          bg="transparent"
          borderWidth={0}
          color={authColors.textStrong}
          flex={1}
          fontSize="4"
          h="100%"
          p={0}
          placeholder={placeholder}
          placeholderTextColor="gray-400"
          returnKeyType={returnKeyType}
          secureTextEntry={secureTextEntry && !revealed}
          value={value}
          onBlur={() => {
            setFocused(false);
            onBlur?.();
          }}
          onChangeText={onChangeText}
          onFocus={() => setFocused(true)}
          onSubmitEditing={onSubmitEditing}
        />

        {secureTextEntry ? (
          <Pressable
            accessibilityLabel={revealed ? 'Hide password' : 'Show password'}
            accessibilityRole="button"
            hitSlop={10}
            onPress={() => setRevealed((current) => !current)}>
            <Ionicons
              color={authColors.textMuted}
              name={revealed ? 'eye-outline' : 'eye-off-outline'}
              size={19}
            />
          </Pressable>
        ) : null}
      </XStack>

      {error ? (
        <Text color="#B91C1C" fontSize="2">
          {error}
        </Text>
      ) : null}
    </YStack>
  );
}
