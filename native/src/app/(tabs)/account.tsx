import { Ionicons } from '@expo/vector-icons';
import { Button, ScrollView, Spinner, Text, XStack, YStack } from 'tamagui';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useCurrentUser } from '@/modules/auth/api/use-current-user';
import { useLogout } from '@/modules/auth/api/use-logout';

/**
 * The account tab: the one screen that proves the authenticated path works end
 * to end — `useCurrentUser` only resolves because the transport attached a
 * bearer token that the auth service accepted, and signing out revokes the
 * refresh token before the guards send the user back to the login screen.
 */
export default function AccountScreen() {
  const insets = useSafeAreaInsets();
  const user = useCurrentUser();
  const logout = useLogout();

  const initials = (user.data?.name || user.data?.email || '?').trim().charAt(0).toUpperCase();

  return (
    <ScrollView bg="background" contentContainerStyle={{ paddingBottom: insets.bottom + 132 }}>
      <YStack gap="6" px="5" pt={insets.top + 24}>
        <YStack gap="1">
          <Text color="color" fontSize="7" fontWeight="700">
            Welcome!
          </Text>
          <Text color="gray-500 dark:gray-400" fontSize="3">
            Your account and sessions for this device.
          </Text>
        </YStack>

        <XStack
          gap="3"
          items="center"
          p="4"
          rounded="2xl"
          bg="gray-50 dark:gray-900"
          borderColor="gray-200 dark:gray-800"
          borderWidth={1}>
          <XStack
            items="center"
            justify="center"
            w="12"
            h="12"
            rounded="full"
            bg="blue-100 dark:blue-900">
            <Text color="blue-700 dark:blue-100" fontSize="5" fontWeight="700">
              {initials}
            </Text>
          </XStack>

          <YStack flex={1} gap="0-5">
            {user.isPending ? (
              <XStack gap="2" items="center">
                <Spinner color="gray-500" size="small" />
                <Text color="gray-500 dark:gray-400" fontSize="3">
                  Loading your profile…
                </Text>
              </XStack>
            ) : user.isError ? (
              <>
                <Text color="color" fontSize="4" fontWeight="700">
                  Could not load your profile
                </Text>
                <Text color="gray-500 dark:gray-400" fontSize="3">
                  {user.error instanceof Error ? user.error.message : 'Unknown error'}
                </Text>
                <Button
                  bg="transparent"
                  borderColor="gray-300 dark:gray-700"
                  borderWidth={1}
                  mt="2"
                  self="flex-start"
                  size="sm"
                  onPress={() => void user.refetch()}>
                  Try again
                </Button>
              </>
            ) : (
              <>
                <Text color="color" fontSize="4" fontWeight="700">
                  {user.data?.name || 'Signed in'}
                </Text>
                <Text color="gray-500 dark:gray-400" fontSize="3">
                  {user.data?.email}
                </Text>
              </>
            )}
          </YStack>
        </XStack>

        <YStack gap="2">
          <XStack gap="3" items="center" py="3">
            <Ionicons color="#6B7280" name="shield-checkmark-outline" size={20} />
            <YStack flex={1}>
              <Text color="color" fontSize="3" fontWeight="600">
                Session
              </Text>
              <Text color="gray-500 dark:gray-400" fontSize="2">
                The refresh token is kept in secure storage on this device only.
              </Text>
            </YStack>
          </XStack>

          <Button
            disabled={logout.isPending}
            gap="2"
            h="12"
            justify="flex-start"
            bg="transparent"
            borderColor="red-200 dark:red-900"
            borderWidth={1}
            rounded="2xl"
            onPress={() => logout.mutate()}>
            <Ionicons color="#DC2626" name="log-out-outline" size={20} />
            <Text color="red-600 dark:red-400" fontSize="4" fontWeight="700">
              {logout.isPending ? 'Signing out…' : 'Log Out'}
            </Text>
          </Button>

          {logout.isError ? (
            <Text color="red-600 dark:red-400" fontSize="2">
              {logout.error instanceof Error ? logout.error.message : 'Sign out failed.'}
            </Text>
          ) : null}
        </YStack>
      </YStack>
    </ScrollView>
  );
}
