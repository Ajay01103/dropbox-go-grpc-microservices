import { Tabs } from 'expo-router';

import { BottomNav } from '@/components/bottom-nav';

export default function TabsLayout() {
  return (
    <Tabs tabBar={(props) => <BottomNav {...props} />} screenOptions={{ headerShown: false }}>
      <Tabs.Screen name="home" />
      <Tabs.Screen name="files" />
      <Tabs.Screen name="photos" />
      <Tabs.Screen name="account" />
    </Tabs>
  );
}
