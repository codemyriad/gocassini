import { get } from "svelte/store";

import { formatCount, LL } from "../i18n";

const MAX_ROOM_NAME_LENGTH = 200;

export interface RoomNameState {
  roomId: string;
  displayName: string;
  defaultName: string;
  customName: string | null;
  revision: number;
}

export class RoomNameError extends Error {
  constructor(message: string, public status: number | null, public current?: RoomNameState) {
    super(message);
    this.name = "RoomNameError";
  }
}

export function validateRoomName(name: string): string | null {
  const text = get(LL).viewer.browse.room_name;
  if (/[\u0000-\u001f\u007f-\u009f]/u.test(name)) return text.invalid_characters();
  const length = [...name.trim()].length;
  return length < 1 || length > MAX_ROOM_NAME_LENGTH ? text.invalid_length({ max: formatCount(MAX_ROOM_NAME_LENGTH) }) : null;
}
