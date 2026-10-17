import { Tabs } from 'expo-router';

import { BottomNav } from '@/components/bottom-nav';
import { TabScrollProvider } from '@/components/tab-scroll';
import { CreateSheetProvider } from '@/modules/upload/components/create-sheet';

/**
 * The tabs layout owns the two pieces of state the screens and the bar share:
 * the create sheet (opened from the tab bar's "+" and the home header) and the
 * scroll offset (written by the active screen, read by the collapsing search
 * field). Neither screen has to thread them through props.
 */
export default function TabsLayout() {
  return (
    <TabScrollProvider>
      <CreateSheetProvider>
        <Tabs
          tabBar={(props) => <BottomNav {...props} />}
          screenOptions={{ headerShown: false }}>
          <Tabs.Screen name="home" />
          <Tabs.Screen name="files" />
          <Tabs.Screen name="photos" />
          <Tabs.Screen name="account" />
        </Tabs>
      </CreateSheetProvider>
    </TabScrollProvider>
  );
}
