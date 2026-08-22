import { createTheme } from '@mui/material/styles'

export const appTheme = createTheme({
  palette: {
    mode: 'light',
    primary: { main: '#174b38', dark: '#10382a', contrastText: '#ffffff' },
    secondary: { main: '#b9dc68', contrastText: '#17201c' },
    error: { main: '#a33c32' },
    warning: { main: '#9a641c' },
    success: { main: '#3d7b43' },
    background: { default: '#edf0eb', paper: '#ffffff' },
    text: { primary: '#1c2521', secondary: '#647069' },
    divider: '#d7ddd7',
  },
  shape: { borderRadius: 8 },
  typography: {
    fontFamily: 'Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
    button: { fontWeight: 700, letterSpacing: 0, textTransform: 'none' },
  },
  components: {
    MuiButton: {
      defaultProps: { disableElevation: true },
      styleOverrides: {
        root: {
          minHeight: '2.5rem',
          borderRadius: '0.52rem',
          fontWeight: 700,
          textTransform: 'none',
        },
      },
    },
    MuiIconButton: {
      styleOverrides: { root: { borderRadius: '0.52rem' } },
    },
    MuiDialog: {
      defaultProps: { transitionDuration: 0 },
    },
    MuiDialogTitle: {
      styleOverrides: { root: { fontSize: '1.5rem', fontWeight: 700 } },
    },
    MuiInputBase: {
      styleOverrides: { root: { fontSize: '0.9rem' } },
    },
    MuiTab: {
      styleOverrides: {
        root: {
          minHeight: '2.75rem',
          fontSize: '0.78rem',
          fontWeight: 750,
          textTransform: 'none',
        },
      },
    },
    MuiTooltip: {
      defaultProps: { arrow: true, enterDelay: 450 },
    },
  },
})

// The workbench is intentionally denser and darker than the authentication
// shell. Keeping it as a nested theme also styles portalled dialogs and
// snackbars, which cannot inherit the workbench's CSS custom properties.
export const workbenchTheme = createTheme(appTheme, {
  palette: {
    mode: 'dark',
    primary: { main: '#77dd84', contrastText: '#102016' },
    secondary: { main: '#65c8e9' },
    error: { main: '#ff9c96' },
    warning: { main: '#e3cf92' },
    background: { default: '#0d1112', paper: '#111718' },
    text: { primary: '#dce5e0', secondary: '#8c9993' },
    divider: '#283132',
  },
  typography: {
    fontSize: 12,
  },
  components: {
    MuiButton: {
      styleOverrides: {
        root: {
          minHeight: 28,
          borderRadius: 4,
          fontSize: '0.72rem',
        },
      },
    },
    MuiDialog: {
      styleOverrides: {
        paper: {
          color: '#dce5e0',
          border: '1px solid #354140',
          backgroundImage: 'none',
        },
      },
    },
    MuiDialogTitle: {
      styleOverrides: { root: { color: '#edf3ef', fontSize: '1rem' } },
    },
    MuiOutlinedInput: {
      styleOverrides: {
        root: {
          color: '#dce5e0',
          background: '#0a0f10',
          '& .MuiOutlinedInput-notchedOutline': { borderColor: '#354140' },
          '&:hover .MuiOutlinedInput-notchedOutline': { borderColor: '#4b5f56' },
        },
      },
    },
  },
})
