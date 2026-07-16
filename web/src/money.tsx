// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Display-precision formatting for exact decimal money strings.
//
// The API transports money as exact decimal strings (D23) and the client
// never does money arithmetic. Formatting here is pure string-digit
// manipulation — no Number(), no float, no BigInt — and is terminal: a
// formatted string is never fed back into any computation. The exact
// wire value stays reachable in the DOM via the <Money> title attribute.

import type { ReactElement } from "react";

const DECIMAL_STRING = /^([+-]?)(\d+)(?:\.(\d+))?$/;

// Increment a digit string by one. Returns the incremented digits and
// whether the increment carried out of the most significant digit
// ("999" → carry with "000").
function incrementDigits(digits: string): { carried: boolean; out: string } {
  const chars = digits.split("");
  for (let i = chars.length - 1; i >= 0; i--) {
    if (chars[i] === "9") {
      chars[i] = "0";
    } else {
      chars[i] = String.fromCharCode(chars[i].charCodeAt(0) + 1);
      return { carried: false, out: chars.join("") };
    }
  }
  return { carried: true, out: chars.join("") };
}

// Round |value| half-up (away from zero) to `digits` fraction digits.
// No-op when the fraction is already that short — never pads.
function roundAt(
  intPart: string,
  fracPart: string,
  digits: number,
): { int: string; frac: string } {
  if (fracPart.length <= digits) {
    return { int: intPart, frac: fracPart };
  }
  const kept = fracPart.slice(0, digits);
  const roundUp = fracPart.charCodeAt(digits) >= 0x35; // "5"
  if (!roundUp) {
    return { int: intPart, frac: kept };
  }
  if (digits === 0) {
    const i = incrementDigits(intPart);
    return { int: (i.carried ? "1" : "") + i.out, frac: "" };
  }
  const f = incrementDigits(kept);
  if (!f.carried) {
    return { int: intPart, frac: f.out };
  }
  const i = incrementDigits(intPart);
  return { int: (i.carried ? "1" : "") + i.out, frac: f.out };
}

/**
 * Formats an exact decimal money string for display: values at or above
 * one (and zero) round half-up to exactly 2 fraction digits, zero-padded
 * so money columns align; values below one keep 4 significant digits
 * (leading fraction zeros + 4) without padding. Integer digits are
 * comma-grouped. Anything that is not a plain decimal string
 * (placeholders like "—") passes through unchanged. `signed` prefixes
 * "+" onto nonzero positive values (for deltas).
 */
export function formatMoney(
  exact: string,
  opts?: { signed?: boolean },
): string {
  const match = DECIMAL_STRING.exec(exact);
  if (!match) {
    return exact;
  }
  const [, sign, rawInt, fracPart = ""] = match;
  const intPart = rawInt.replace(/^0+(?=\d)/, "");
  const isZero = !/[1-9]/.test(intPart + fracPart);

  let rounded: { int: string; frac: string };
  if (isZero) {
    rounded = { int: intPart, frac: "00" };
  } else if (/[1-9]/.test(intPart)) {
    const r = roundAt(intPart, fracPart, 2);
    rounded = { int: r.int, frac: r.frac.padEnd(2, "0") };
  } else {
    const leadingZeros = /^0*/.exec(fracPart)![0].length;
    rounded = roundAt(intPart, fracPart, leadingZeros + 4);
    // A carry can lift a sub-one value to one ("0.99999" → "1.0000");
    // re-round the ORIGINAL at 2 so the ≥1 rule applies without
    // double-rounding drift.
    if (/[1-9]/.test(rounded.int)) {
      rounded = roundAt(intPart, fracPart, 2);
    } else {
      // A carry can also consume a leading zero ("0.099995" → "0.10000",
      // five significant digits). Re-trim to the NEW zero count + 4; the
      // dropped characters are carry-produced zeros, so this is exact.
      const zerosAfter = /^0*/.exec(rounded.frac)![0].length;
      if (zerosAfter < leadingZeros) {
        rounded = {
          int: rounded.int,
          frac: rounded.frac.slice(0, zerosAfter + 4),
        };
      }
    }
  }

  const grouped = rounded.int.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  const body = rounded.frac ? `${grouped}.${rounded.frac}` : grouped;
  if (isZero) {
    return body;
  }
  if (sign === "-") {
    return `-${body}`;
  }
  return opts?.signed ? `+${body}` : body;
}

/**
 * Renders a money string at display precision with the exact wire value
 * (plus currency, when given) preserved in the title attribute. Nullish
 * values render the em-dash placeholder.
 */
export function Money({
  value,
  currency,
  signed,
}: {
  value: string | null | undefined;
  currency?: string;
  signed?: boolean;
}): ReactElement {
  if (value === null || value === undefined || value === "") {
    return <>—</>;
  }
  const exact = currency ? `${value} ${currency}` : value;
  return (
    <span className="money" title={exact}>
      {formatMoney(value, { signed })}
    </span>
  );
}
