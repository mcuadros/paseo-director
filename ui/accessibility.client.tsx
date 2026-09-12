// SPDX-License-Identifier: Apache-2.0
// Shared cross-platform accessibility behavior for Director-owned UI bodies.

import React, {
  createContext,
  forwardRef,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import {
  AccessibilityInfo,
  Pressable,
  type PressableProps,
  type PressableStateCallbackType,
  type StyleProp,
  type View,
  type ViewStyle,
  useWindowDimensions,
} from "react-native";

export const minimumTouchTarget = 44;
export const defaultTouchHitSlop = 4;
export const compactWindowBreakpoint = 1_200;

export function isCompactWindow(
  hostCompact: boolean,
  windowWidth: number,
): boolean {
  return hostCompact || windowWidth < compactWindowBreakpoint;
}

export function useResponsiveCompactLayout(hostCompact: boolean): boolean {
  const { width } = useWindowDimensions();
  return isCompactWindow(hostCompact, width);
}

export type AccessibilityPreferences = {
  reduceMotion: boolean;
  highContrast: boolean;
};

function readOptionalPreference(
  method: (() => Promise<boolean>) | undefined,
): Promise<boolean> {
  return typeof method === "function"
    ? method().catch(() => false)
    : Promise.resolve(false);
}

const defaultPreferences: AccessibilityPreferences = {
  reduceMotion: false,
  highContrast: false,
};

type AccessibilityEnvironment = AccessibilityPreferences & {
  focusColor?: string;
};

const AccessibilityEnvironmentContext = createContext<AccessibilityEnvironment>(
  defaultPreferences,
);

export function useAccessibilityPreferences(): AccessibilityPreferences {
  const [preferences, setPreferences] = useState(defaultPreferences);

  useEffect(() => {
    let active = true;
    let highTextContrastEnabled = false;
    let darkerSystemColorsEnabled = false;
    const updateHighContrast = () =>
      setPreferences((current) => ({
        ...current,
        highContrast: highTextContrastEnabled || darkerSystemColorsEnabled,
      }));
    void Promise.all([
      readOptionalPreference(
        AccessibilityInfo.isReduceMotionEnabled?.bind(AccessibilityInfo),
      ),
      readOptionalPreference(
        AccessibilityInfo.isHighTextContrastEnabled?.bind(AccessibilityInfo),
      ),
      readOptionalPreference(
        AccessibilityInfo.isDarkerSystemColorsEnabled?.bind(AccessibilityInfo),
      ),
    ]).then(([reduceMotion, highTextContrast, darkerSystemColors]) => {
      if (active) {
        highTextContrastEnabled = highTextContrast;
        darkerSystemColorsEnabled = darkerSystemColors;
        setPreferences({
          reduceMotion,
          highContrast:
            highTextContrastEnabled || darkerSystemColorsEnabled,
        });
      }
    });

    const reduceMotion = AccessibilityInfo.addEventListener(
      "reduceMotionChanged",
      (enabled) =>
        setPreferences((current) => ({ ...current, reduceMotion: enabled })),
    );
    const highTextContrast = AccessibilityInfo.addEventListener(
      "highTextContrastChanged",
      (enabled) => {
        highTextContrastEnabled = enabled;
        updateHighContrast();
      },
    );
    const darkerSystemColors = AccessibilityInfo.addEventListener(
      "darkerSystemColorsChanged",
      (enabled) => {
        darkerSystemColorsEnabled = enabled;
        updateHighContrast();
      },
    );

    return () => {
      active = false;
      reduceMotion?.remove?.();
      highTextContrast?.remove?.();
      darkerSystemColors?.remove?.();
    };
  }, []);

  return preferences;
}

export function AccessibilityProvider({
  children,
  focusColor,
  preferences,
}: {
  children?: React.ReactNode;
  focusColor: string;
  preferences: AccessibilityPreferences;
}) {
  const value = useMemo(
    () => ({ ...preferences, focusColor }),
    [focusColor, preferences],
  );
  return (
    <AccessibilityEnvironmentContext.Provider value={value}>
      {children}
    </AccessibilityEnvironmentContext.Provider>
  );
}

export function useAccessibilityAnnouncement(message: string | null): void {
  const previous = useRef<string | null>(null);
  useEffect(() => {
    if (!message || previous.current === message) return;
    previous.current = message;
    if (
      typeof AccessibilityInfo.announceForAccessibilityWithOptions ===
      "function"
    ) {
      AccessibilityInfo.announceForAccessibilityWithOptions(message, {
        queue: true,
      });
    } else {
      AccessibilityInfo.announceForAccessibility(message);
    }
  }, [message]);
}

export type AccessiblePressableProps = PressableProps & {
  focusColor?: string;
};

export const AccessiblePressable = forwardRef<View, AccessiblePressableProps>(
  function AccessiblePressable(
    {
      accessibilityRole = "button",
      accessibilityState,
      children,
      disabled,
      focusColor,
      focusable,
      hitSlop = defaultTouchHitSlop,
      onBlur,
      onFocus,
      style,
      ...props
    },
    ref,
  ) {
    const environment = useContext(AccessibilityEnvironmentContext);
    const [focused, setFocused] = useState(false);
    const resolvedFocusColor = focusColor ?? environment.focusColor;

    function resolvedStyle(
      state: PressableStateCallbackType,
    ): StyleProp<ViewStyle> {
      const supplied = typeof style === "function" ? style(state) : style;
      return [
        { minHeight: minimumTouchTarget, minWidth: minimumTouchTarget },
        supplied,
        !disabled && state.pressed ? { opacity: 0.72 } : null,
        focused && resolvedFocusColor
          ? {
              outlineColor: resolvedFocusColor,
              outlineOffset: 2,
              outlineStyle: "solid",
              outlineWidth: environment.highContrast ? 3 : 2,
            }
          : null,
      ];
    }

    return (
      <Pressable
        {...props}
        accessibilityRole={accessibilityRole}
        accessibilityState={
          disabled
            ? { ...accessibilityState, disabled: true }
            : accessibilityState
        }
        disabled={disabled}
        focusable={focusable ?? !disabled}
        hitSlop={hitSlop}
        onBlur={(event) => {
          setFocused(false);
          onBlur?.(event);
        }}
        onFocus={(event) => {
          setFocused(true);
          onFocus?.(event);
        }}
        ref={ref}
        style={resolvedStyle}
      >
        {children}
      </Pressable>
    );
  },
);
