import { Ionicons } from "@expo/vector-icons";
import { Tabs } from "expo-router";
import { useEffect, useState, type ComponentProps, type ReactNode } from "react";
import { Pressable, StyleSheet, useColorScheme } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import Animated, {
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withSpring,
  withTiming,
} from "react-native-reanimated";
import { Button, Input, Text, XStack, YStack } from "tamagui";

type TabsProps = ComponentProps<typeof Tabs>;

export type TabBarProps =
  Exclude<TabsProps["tabBar"], undefined> extends (props: infer P) => ReactNode ? P : never;

type IconName = ComponentProps<typeof Ionicons>["name"];
type TabMeta = { label: string; outline: IconName; filled: IconName };

// Keys must match the file names in src/app/(tabs)/. Routes not listed here are
// ignored by the bar.
const TAB_META: Record<string, TabMeta> = {
  home: { label: "Home", outline: "home-outline", filled: "home" },
  files: { label: "Files", outline: "folder-outline", filled: "folder" },
  photos: { label: "Photos", outline: "image-outline", filled: "image" },
  account: { label: "Account", outline: "person-outline", filled: "person" },
};

// Tailwind gray-900/50 and gray-500/400. Animated values need concrete strings.
const PALETTE = {
  light: { active: "#111827", inactive: "#6B7280" },
  dark: { active: "#F9FAFB", inactive: "#9CA3AF" },
} as const;

const SPRING = { damping: 16, stiffness: 320 } as const;

const iconCenter = StyleSheet.create({
  center: { alignItems: "center", justifyContent: "center" },
});

type TabButtonProps = {
  meta: TabMeta;
  focused: boolean;
  colors: (typeof PALETTE)[keyof typeof PALETTE];
  reduceMotion: boolean | null;
  onPress: () => void;
  onLongPress: () => void;
};

function TabButton({ meta, focused, colors, reduceMotion, onPress, onLongPress }: TabButtonProps) {
  const progress = useSharedValue(focused ? 1 : 0);
  const press = useSharedValue(1);

  useEffect(() => {
    progress.value = reduceMotion
      ? focused
        ? 1
        : 0
      : withTiming(focused ? 1 : 0, { duration: 240 });
  }, [focused, progress, reduceMotion]);

  const pillStyle = useAnimatedStyle(() => ({
    opacity: progress.value,
    transform: [{ scale: 0.75 + progress.value * 0.25 }],
  }));
  const outlineStyle = useAnimatedStyle(() => ({ opacity: 1 - progress.value }));
  const filledStyle = useAnimatedStyle(() => ({ opacity: progress.value }));
  const pressStyle = useAnimatedStyle(() => ({ transform: [{ scale: press.value }] }));

  return (
    <Animated.View style={[pressStyle, styles.tabItem]}>
      <Pressable
        style={styles.tabPressable}
        onPress={onPress}
        onLongPress={onLongPress}
        onPressIn={() => {
          press.set(reduceMotion ? 1 : withSpring(0.92, SPRING));
        }}
        onPressOut={() => {
          press.set(withSpring(1, SPRING));
        }}
        accessibilityRole="tab"
        accessibilityLabel={meta.label}
        accessibilityState={{ selected: focused }}
      >
        <YStack
          pointerEvents="none"
          w="100%"
          items="center"
          gap="0-5"
          px="3"
          py="1"
          rounded="full"
          bg="transparent hover:gray-100 dark:hover:gray-800"
        >
          <YStack w="16" h="8" items="center" justify="center">
            <Animated.View style={[StyleSheet.absoluteFill, pillStyle]}>
              <YStack w="100%" h="100%" rounded="full" bg="blue-100 dark:blue-900" />
            </Animated.View>
            <Animated.View style={[StyleSheet.absoluteFill, iconCenter.center, outlineStyle]}>
              <Ionicons name={meta.outline} size={22} color={colors.inactive} />
            </Animated.View>
            <Animated.View style={[StyleSheet.absoluteFill, iconCenter.center, filledStyle]}>
              <Ionicons name={meta.filled} size={22} color={colors.active} />
            </Animated.View>
          </YStack>
          <Text
            fontSize="2"
            fontWeight={focused ? "700" : "500"}
            color={focused ? "color" : "gray-500 dark:gray-400"}
          >
            {meta.label}
          </Text>
        </YStack>
      </Pressable>
    </Animated.View>
  );
}

function CreateButton({
  reduceMotion,
  onPress,
}: {
  reduceMotion: boolean | null;
  onPress: () => void;
}) {
  const scale = useSharedValue(1);
  const pressStyle = useAnimatedStyle(() => ({ transform: [{ scale: scale.value }] }));

  return (
    <Animated.View style={pressStyle}>
      <Pressable
        onPress={onPress}
        onPressIn={() => {
          scale.set(reduceMotion ? 1 : withSpring(0.9, SPRING));
        }}
        onPressOut={() => {
          scale.set(withSpring(1, SPRING));
        }}
        accessibilityRole="button"
        accessibilityLabel="Create new"
        hitSlop={4}
      >
        <YStack
          pointerEvents="none"
          w="14"
          h="14"
          rounded="full"
          bg="blue-500"
          items="center"
          justify="center"
          shadowColor="#1D4ED8"
          shadowOpacity={0.3}
          shadowRadius={10}
          shadowOffset={{ width: 0, height: 4 }}
        >
          <Ionicons name="add" size={26} color="#FFFFFF" />
        </YStack>
      </Pressable>
    </Animated.View>
  );
}

function SearchBar({ iconColor }: { iconColor: string }) {
  const [query, setQuery] = useState("");

  return (
    <XStack w="100%" maxW="640px" mx="auto" mb="2" px="2" items="center">
      <XStack
        flex={1}
        minW={0}
        h="14"
        px="3"
        items="center"
        gap="2"
        rounded="full"
        bg="background"
        borderWidth={1}
        borderColor="gray-200 dark:gray-800"
        shadowColor="#000000"
        shadowOpacity={0.08}
        shadowRadius={8}
        shadowOffset={{ width: 0, height: 2 }}
      >
        <Ionicons name="search" size={24} color={iconColor} />
        <Input
          flex={1}
          minW={0}
          p={0}
          borderWidth={0}
          bg="transparent"
          value={query}
          onChangeText={setQuery}
          placeholder="Search for anything"
          placeholderTextColor="gray-500"
          fontSize="4"
          color="color"
          accessibilityLabel="Search for anything"
        />
        <Button
          circular
          size={false}
          p={0}
          bg="transparent"
          borderWidth={0}
          icon={<Ionicons name="mic-outline" size={22} color={iconColor} />}
          accessibilityLabel="Voice search"
        />
      </XStack>
    </XStack>
  );
}

export function BottomNav({ state, navigation }: TabBarProps) {
  const insets = useSafeAreaInsets();
  const scheme = useColorScheme();
  const reduceMotion = useReducedMotion();
  const colors = PALETTE[scheme === "dark" ? "dark" : "light"];

  const focusedKey = state.routes[state.index]?.key;

  const goTo = (routeName: string) => {
    const route = state.routes.find((r) => r.name === routeName);
    if (!route) return;

    const event = navigation.emit({
      type: "tabPress",
      target: route.key,
      canPreventDefault: true,
    });

    if (route.key !== focusedKey && !event.defaultPrevented) {
      navigation.navigate(route.name, route.params);
    }
  };

  const tabs = state.routes.filter((r) => r.name in TAB_META);
  const left = tabs.slice(0, 2);
  const right = tabs.slice(2);

  const renderTab = (route: (typeof tabs)[number]) => (
    <TabButton
      key={route.key}
      meta={TAB_META[route.name]}
      focused={route.key === focusedKey}
      colors={colors}
      reduceMotion={reduceMotion}
      onPress={() => goTo(route.name)}
      onLongPress={() => navigation.emit({ type: "tabLongPress", target: route.key })}
    />
  );

  return (
    <YStack bg="background">
      <SearchBar iconColor={colors.active} />
      <YStack h="px" bg="border-color" />
      <XStack
        w="100%"
        maxW="640px"
        mx="auto"
        items="center"
        justify="space-between"
        px="2"
        pt="1.5"
        pb={insets.bottom + 8}
      >
        {left.map(renderTab)}
        <CreateButton reduceMotion={reduceMotion} onPress={() => goTo("files")} />
        {right.map(renderTab)}
      </XStack>
    </YStack>
  );
}

const styles = StyleSheet.create({
  tabItem: {
    flex: 1,
    alignItems: "center",
    minWidth: 0,
  },
  tabPressable: {
    flex: 1,
    width: "100%",
    minHeight: 64,
    alignItems: "center",
    justifyContent: "center",
  },
});
