import { Button, Group, Modal, Stack, Text } from "@mantine/core";
import { type ReactNode, useState } from "react";

/**
 * A red button for something that cannot be undone: it opens a dialog saying what happens, and
 * acts only on the dialog's own confirm (in the page, unlike window.confirm, so it is styled,
 * keyboard-reachable and testable). The message can be built from data fetched while the dialog is
 * open (onOpenChange), and confirm stays disabled while something blocks the action.
 */
export function ConfirmButton({
  label,
  title,
  message,
  confirmLabel,
  isPending,
  isConfirmDisabled = false,
  onOpenChange,
  onConfirm,
}: {
  label: string;
  title: string;
  message: ReactNode;
  confirmLabel: string;
  isPending: boolean;
  isConfirmDisabled?: boolean;
  onOpenChange?: (isOpen: boolean) => void;
  onConfirm: () => void;
}) {
  const [isOpen, setOpen] = useState(false);
  const setIsOpen = (open: boolean) => {
    setOpen(open);
    onOpenChange?.(open);
  };
  return (
    <>
      <Button color="red" variant="light" loading={isPending} onClick={() => setIsOpen(true)}>
        {label}
      </Button>
      <Modal opened={isOpen} onClose={() => setIsOpen(false)} title={title} centered>
        <Stack gap="md">
          {typeof message === "string" ? <Text size="sm">{message}</Text> : message}
          <Group justify="flex-end">
            <Button variant="default" onClick={() => setIsOpen(false)}>
              Cancel
            </Button>
            <Button
              color="red"
              disabled={isConfirmDisabled}
              onClick={() => {
                setIsOpen(false);
                onConfirm();
              }}
            >
              {confirmLabel}
            </Button>
          </Group>
        </Stack>
      </Modal>
    </>
  );
}
