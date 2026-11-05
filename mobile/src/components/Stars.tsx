// Five stars, each half a point of ten (tap the left half for a half star).
import { Pressable, Text, View } from "react-native";

import { colors } from "../lib/theme";
import { Icon } from "./Icon";
import { tap } from "./ui";

const WORDS: Record<number, string> = { 1: "Awful", 2: "Very bad", 3: "Bad", 4: "Poor", 5: "Meh", 6: "Fine", 7: "Good", 8: "Great", 9: "Superb", 10: "Masterpiece" };

export const ratingWord = (r?: number | null) => (r ? `${r}/10 · ${WORDS[r]}` : "");

export function Stars({ value, onChange, size = 46 }: { value: number; onChange: (v: number) => void; size?: number }) {
  return (
    <View style={{ flexDirection: "row", justifyContent: "center", gap: 6 }} accessibilityRole="adjustable" accessibilityValue={{ min: 0, max: 10, now: value }}>
      {[0, 1, 2, 3, 4].map((i) => {
        const full = value >= (i + 1) * 2;
        const half = !full && value === i * 2 + 1;
        return (
          <Pressable
            key={i}
            accessibilityLabel={`${i + 1} stars`}
            onPress={(e) => { tap(); onChange(e.nativeEvent.locationX < size / 2 ? i * 2 + 1 : i * 2 + 2); }}
            style={{ width: size + 6, height: size + 10, alignItems: "center", justifyContent: "center" }}
          >
            <Icon name={full ? "starFill" : "star"} size={size} color={full || half ? colors.accent : "#5b6272"} />
            {half ? (
              <View style={{ position: "absolute", left: 3, width: size / 2, height: size, overflow: "hidden" }}>
                <Icon name="starFill" size={size} color={colors.accent} />
              </View>
            ) : null}
          </Pressable>
        );
      })}
    </View>
  );
}

export function RatingLabel({ value }: { value: number }) {
  return (
    <Text style={{ color: colors.text, textAlign: "center", fontSize: 17, fontWeight: "700", marginTop: 6, minHeight: 26 }}>
      {ratingWord(value) || "Tap a star — left half is half a star"}
    </Text>
  );
}
