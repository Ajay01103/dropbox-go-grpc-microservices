import { StyleSheet, Text, View, useColorScheme } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

type Props = { title: string; subtitle?: string };

export function ScreenPlaceholder({ title, subtitle }: Props) {
  const dark = useColorScheme() === "dark";
  const insets = useSafeAreaInsets();

  return (
    <View
      style={[
        styles.container,
        { paddingTop: insets.top, backgroundColor: dark ? "#0B0F19" : "#FFFFFF" },
      ]}
    >
      <Text style={[styles.title, { color: dark ? "#F9FAFB" : "#111827" }]}>{title}</Text>
      {subtitle ? <Text style={styles.subtitle}>{subtitle}</Text> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, alignItems: "center", justifyContent: "center", paddingHorizontal: 24 },
  title: { fontSize: 28, fontWeight: "700" },
  subtitle: { marginTop: 8, fontSize: 15, color: "#6B7280", textAlign: "center" },
});
