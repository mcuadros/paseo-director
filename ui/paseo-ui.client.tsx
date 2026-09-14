// SPDX-License-Identifier: Apache-2.0
// Client primitives implemented with the React Native surface accepted by the
// exact Paseo 0.7.2 desktop client. No injected plugin bridge is required.

import React, { type ComponentType, type ReactNode } from "react";
import {
  AccessibilityInfo,
  Modal as NativeModal,
  Pressable,
  StyleSheet,
  Text,
  View,
} from "react-native";

type Theme = {
  colors: {
    surface1: string;
    foreground: string;
    foregroundMuted: string;
    border: string;
  };
};

type ModalProps = {
  title: string;
  icon?: ReactNode;
  open: boolean;
  onOpenChange(open: boolean): void;
  children: ReactNode;
  theme: Theme;
};

type ModalComponent = React.FunctionComponent<ModalProps> & {
  Content: ComponentType<{ children: ReactNode }>;
};

export const Modal = (({ title, icon, open, onOpenChange, children, theme }: ModalProps) => (
  <NativeModal
    animationType="fade"
    onRequestClose={() => onOpenChange(false)}
    presentationStyle="overFullScreen"
    transparent
    visible={open}
  >
    <View accessibilityViewIsModal style={styles.backdrop}>
      <View style={[styles.dialog, { backgroundColor: theme.colors.surface1, borderColor: theme.colors.border }]}>
        <View style={styles.header}>
          {icon}
          <Text accessibilityRole="header" style={[styles.title, { color: theme.colors.foreground }]}>{title}</Text>
          <Pressable
            accessibilityHint="Closes this dialog"
            accessibilityLabel={`Close ${title}`}
            accessibilityRole="button"
            hitSlop={8}
            onPress={() => onOpenChange(false)}
            style={styles.close}
          >
            <Text style={[styles.closeText, { color: theme.colors.foregroundMuted }]}>×</Text>
          </Pressable>
        </View>
        {children}
      </View>
    </View>
  </NativeModal>
)) as ModalComponent;

Modal.Content = function ModalContent({ children }: { children: ReactNode }) {
  return <View style={styles.content}>{children}</View>;
};

function announce(message: string): void {
  void AccessibilityInfo.announceForAccessibility(message);
}

export function useToast() {
  return {
    show(message: string, _options?: { variant?: "default" | "info" | "success" | "warning" | "error"; durationMs?: number }) { announce(message); },
    error(message: string) { announce(message); },
  };
}

const styles = StyleSheet.create({
  backdrop: {
    alignItems: "center",
    backgroundColor: "rgba(0, 0, 0, 0.55)",
    flex: 1,
    justifyContent: "center",
    padding: 20,
  },
  dialog: {
    borderRadius: 12,
    borderWidth: StyleSheet.hairlineWidth,
    maxHeight: "90%",
    maxWidth: 760,
    overflow: "hidden",
    width: "100%",
  },
  header: {
    alignItems: "center",
    flexDirection: "row",
    gap: 10,
    minHeight: 52,
    paddingHorizontal: 16,
  },
  title: { flex: 1, fontSize: 17, fontWeight: "700" },
  close: { alignItems: "center", justifyContent: "center", minHeight: 40, minWidth: 40 },
  closeText: { fontSize: 26, lineHeight: 28 },
  content: { flexShrink: 1 },
});
